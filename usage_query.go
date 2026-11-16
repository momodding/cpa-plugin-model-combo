package main

import (
	"container/heap"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type usageRouterKey struct{ model, attribution string }
type usageGroupKey struct {
	value  string
	router usageRouterKey
}

type usagePageOptions struct {
	Sort, Order   string
	Offset, Limit int
}

type usageGroupOptions struct {
	Dimension string
	usagePageOptions
}

func (store *usageStore) Overview(filter usageFilter, granularity string) (usageOverview, error) {
	if granularity == "" {
		granularity = "hour"
	}
	result, err := store.queryUsage(filter, granularity, nil, nil)
	return result.Overview, err
}

func (store *usageStore) Groups(filter usageFilter, dimension, sortField, order string, offset, limit int) (usageGroupPage, error) {
	result, err := store.queryUsage(filter, "", &usageGroupOptions{dimension, usagePageOptions{sortField, order, offset, limit}}, nil)
	return result.Groups, err
}

func (store *usageStore) Requests(filter usageFilter, sortField, order string, offset, limit int) (usageRequestPage, error) {
	result, err := store.queryUsage(filter, "", nil, &usagePageOptions{sortField, order, offset, limit})
	return result.Requests, err
}

func (store *usageStore) Dashboard(filter usageFilter, granularity string, groups usageGroupOptions, requests usagePageOptions) (usageDashboard, error) {
	if granularity == "" {
		granularity = "hour"
	}
	return store.queryUsage(filter, granularity, &groups, &requests)
}

func (store *usageStore) queryUsage(filter usageFilter, granularity string, groups *usageGroupOptions, requests *usagePageOptions) (usageDashboard, error) {
	var result usageDashboard
	var diagnostics *usageAccountingDiagnostics
	if granularity != "" && nativeDiagnosticsAvailable() {
		diagnostics = &usageAccountingDiagnostics{operation: diagnosticOperationID(), started: time.Now(), filter: filter, groups: make(map[accountingDiagnosticKey]accountingDiagnosticTotals)}
	}
	err := store.readSnapshot(func(connection *sql.Conn, retentionDays int) error {
		book, err := priceBookFromSQLite(connection)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		result = usageDashboard{SchemaVersion: usageManagementSchemaVersion, GeneratedAt: now, PriceBookRevision: book.Revision}
		resolver := newModelPriceResolver(book.Prices, book.SyncSettings)
		var overview *usageOverviewAccumulator
		if granularity != "" {
			overview = newUsageOverviewAccumulator(filter, granularity, now, retentionDays, store.LastError())
		}
		var grouped *usageGroupsAccumulator
		if groups != nil {
			grouped = &usageGroupsAccumulator{dimension: groups.Dimension, groups: make(map[usageGroupKey]*groupAccumulator)}
		}
		withCosts := overview != nil || grouped != nil
		var selection *usageRequestSelection
		if requests != nil {
			selection = newUsageRequestSelection(*requests, filter)
		}
		var sessions map[string][]storedRequestSession
		if overview != nil || grouped != nil || selection != nil {
			sessions = make(map[string][]storedRequestSession)
			if err := scanRequestSessions(connection, func(session storedRequestSession) {
				if session.TraceID != "" {
					sessions[session.TraceID] = append(sessions[session.TraceID], session)
				}
			}); err != nil {
				return err
			}
		}
		if overview != nil || grouped != nil {
			err := scanUsageRecords(connection, usageFilter{From: filter.From, To: filter.To}, func(record storedUsageRecord) {
				if matching := sessions[record.TraceID]; record.TraceID != "" && len(matching) == 1 {
					record = sessionAttributedRecord(record, matching[0])
				}
				if !filter.matches(record) {
					return
				}
				var cost estimatedCost
				if withCosts {
					cost = estimateUsageCost(record, resolver)
				}
				if diagnostics != nil {
					diagnostics.add(record, cost)
				}
				if overview != nil {
					overview.add(record, cost)
				}
				if grouped != nil {
					grouped.add(record, cost)
				}
			})
			if err != nil {
				return err
			}
		}
		if selection != nil {
			if err := scanUsageRecords(connection, usageFilter{}, func(record storedUsageRecord) {
				selection.add(record, estimateUsageCost(record, resolver))
			}); err != nil {
				return err
			}
			selection.sessions = sessions
		}
		if overview != nil {
			result.Overview = overview.finish()
		}
		if grouped != nil {
			result.Groups = grouped.finish(*groups, now)
		}
		if selection != nil {
			result.Requests = selection.finish(book.Revision, now)
		}
		return nil
	})
	if diagnostics != nil {
		diagnostics.finish(result.PriceBookRevision, err)
	}
	if err != nil {
		store.recordError(err)
		return usageDashboard{}, err
	}
	return result, nil
}

func (filter usageFilter) hasDimensions() bool {
	return filter.Attribution != "" || filter.RouterModel != "" || filter.ProviderModel != "" || filter.Source != "" || filter.ServiceTier != "" || filter.Result != ""
}

func countUsageRecords(connection *sql.Conn, filter usageFilter) (int, error) {
	where, args := usageTimeWhere(filter)
	var count int
	err := connection.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM requests`+where, args...).Scan(&count)
	return count, err
}

func indexedUsageRequestPage(connection *sql.Conn, filter usageFilter, options usagePageOptions, resolver modelPriceResolver) ([]usageRequestDetail, error) {
	where, args := usageTimeWhere(filter)
	order := " DESC"
	if options.Order == "asc" {
		order = " ASC"
	}
	rows, err := connection.QueryContext(context.Background(), `SELECT payload FROM requests`+where+` ORDER BY requested_at_ns`+order+`, sequence`+order+` LIMIT ? OFFSET ?`, append(args, options.Limit, options.Offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]usageRequestDetail, 0)
	var value sql.RawBytes
	for rows.Next() {
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		var record storedUsageRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return nil, fmt.Errorf("decode usage record: %w", err)
		}
		items = append(items, requestDetail(record, resolver))
	}
	return items, rows.Err()
}

type usageOverviewAccumulator struct {
	overview                       usageOverview
	granularity                    string
	series                         map[string]*usageSeriesPoint
	routerModels                   map[usageRouterKey]*usageModelStats
	providerModels                 map[string]*usageModelStats
	sources, serviceTiers, results map[string]struct{}
}

func newUsageOverviewAccumulator(filter usageFilter, granularity string, now time.Time, retentionDays int, storageError string) *usageOverviewAccumulator {
	return &usageOverviewAccumulator{
		overview:    usageOverview{SchemaVersion: usageManagementSchemaVersion, GeneratedAt: now, From: filter.From, To: filter.To, RetainedSince: now.AddDate(0, 0, -retentionDays), StorageError: storageError},
		granularity: granularity, series: make(map[string]*usageSeriesPoint), routerModels: make(map[usageRouterKey]*usageModelStats), providerModels: make(map[string]*usageModelStats),
		sources: make(map[string]struct{}), serviceTiers: make(map[string]struct{}), results: make(map[string]struct{}),
	}
}

func (acc *usageOverviewAccumulator) add(record storedUsageRecord, cost estimatedCost) {
	overview, granularity, series := &acc.overview, acc.granularity, acc.series
	routerModels, providerModels := acc.routerModels, acc.providerModels
	sources, serviceTiers, results := acc.sources, acc.serviceTiers, acc.results

	overview.Summary.add(record)
	overview.Costs.add(cost)
	bucket := usageBucket(record.RequestedAt, granularity)
	key := bucket.Format(time.RFC3339)
	point := series[key]
	if point == nil {
		point = &usageSeriesPoint{Time: key}
		series[key] = point
	}
	point.usageCounters.add(record)
	mode := effectiveAccountingMode(record, cost.AccountingMode)
	if mode == accountingModeInputIncludesCache {
		point.CacheReadIncludedTokens += effectiveCacheReadTokens(record)
	}
	if reasoningIncludedInOutput(record) {
		point.ReasoningIncludedTokens += record.ReasoningTokens
	}
	point.usageTimingTotals.add(record)
	point.CostUSD += cost.TotalUSD

	routerKey := usageRouterKey{model: record.RouterModel, attribution: record.Attribution}
	routerLabel, routerAttribution := record.RouterModel, record.Attribution
	if record.Attribution == attributionDirect {
		routerKey.model, routerLabel = "", ""
	} else if record.Attribution == attributionUnresolved {
		routerKey.model, routerLabel = "", ""
	}
	router := routerModels[routerKey]
	if router == nil {
		router = &usageModelStats{Model: routerLabel, Attribution: routerAttribution}
		routerModels[routerKey] = router
	}
	router.usageCounters.add(record)
	router.CostUSD += cost.TotalUSD

	provider := providerModels[record.ProviderModel]
	if provider == nil {
		provider = &usageModelStats{Model: record.ProviderModel}
		providerModels[record.ProviderModel] = provider
	}
	provider.usageCounters.add(record)
	provider.CostUSD += cost.TotalUSD
	if record.Source != "" {
		sources[record.Source] = struct{}{}
	}
	if record.ServiceTier != "" {
		serviceTiers[record.ServiceTier] = struct{}{}
	}
	results[record.result()] = struct{}{}
}

func (acc *usageOverviewAccumulator) finish() usageOverview {
	overview, series := &acc.overview, acc.series
	routerModels, providerModels := acc.routerModels, acc.providerModels
	sources, serviceTiers, results := acc.sources, acc.serviceTiers, acc.results
	for _, point := range series {
		point.AverageLatencyNS, point.AverageTTFTNS, point.AverageTPS = point.usageTimingTotals.averages(point.Requests)
		overview.Series = append(overview.Series, *point)
	}
	sort.Slice(overview.Series, func(left, right int) bool { return overview.Series[left].Time < overview.Series[right].Time })
	overview.RouterModels = modelStatsValues(routerModels)
	overview.ProviderModels = modelStatsValues(providerModels)
	overview.Sources = sortedStrings(sources)
	overview.ServiceTiers = sortedStrings(serviceTiers)
	overview.Results = sortedStrings(results)
	return *overview
}

func usageBucket(value time.Time, granularity string) time.Time {
	value = value.UTC()
	switch granularity {
	case "minute":
		return value.Truncate(time.Minute)
	case "day":
		return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
	default:
		return value.Truncate(time.Hour)
	}
}

func modelStatsValues[K comparable](values map[K]*usageModelStats) []usageModelStats {
	result := make([]usageModelStats, 0, len(values))
	for _, value := range values {
		result = append(result, *value)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].TotalTokens != result[right].TotalTokens {
			return result[left].TotalTokens > result[right].TotalTokens
		}
		return strings.ToLower(result[left].Model) < strings.ToLower(result[right].Model)
	})
	return result
}

type groupAccumulator struct {
	group usageGroup
	usageTimingTotals
}

type usageGroupsAccumulator struct {
	dimension string
	groups    map[usageGroupKey]*groupAccumulator
}

func (acc *usageGroupsAccumulator) add(record storedUsageRecord, cost estimatedCost) {
	dimension, groups := acc.dimension, acc.groups

	value := usageDimensionValue(record, dimension)
	key := usageGroupKey{value: value}
	if dimension == "router_model" {
		key = usageGroupKey{router: usageRouterKey{model: record.RouterModel, attribution: record.Attribution}}
	}
	accumulator := groups[key]
	if accumulator == nil {
		accumulator = &groupAccumulator{group: usageGroup{Key: value}}
		assignGroupDimension(&accumulator.group, record, dimension)
		groups[key] = accumulator
	}
	accumulator.group.usageCounters.add(record)
	accumulator.usageTimingTotals.add(record)
	accumulator.group.CostUSD += cost.TotalUSD
	if cost.Priced {
		accumulator.group.PricedRequests++
	}
}

func (acc *usageGroupsAccumulator) finish(options usageGroupOptions, now time.Time) usageGroupPage {
	groups, dimension, sortField, order, offset, limit := acc.groups, options.Dimension, options.Sort, options.Order, options.Offset, options.Limit
	items := make([]usageGroup, 0, len(groups))
	for _, accumulator := range groups {
		accumulator.group.AverageLatencyNS, accumulator.group.AverageTTFTNS, accumulator.group.AverageTPS = accumulator.usageTimingTotals.averages(accumulator.group.Requests)
		items = append(items, accumulator.group)
	}
	sort.SliceStable(items, func(left, right int) bool {
		comparison := compareGroups(items[left], items[right], sortField)
		if comparison == 0 {
			comparison = strings.Compare(strings.ToLower(items[left].Key), strings.ToLower(items[right].Key))
		}
		if order == "asc" {
			return comparison < 0
		}
		return comparison > 0
	})
	total := len(items)
	items = paginateGroups(items, offset, limit)
	return usageGroupPage{SchemaVersion: usageManagementSchemaVersion, GeneratedAt: now, Dimension: dimension, Total: total, Offset: offset, Limit: limit, Items: items}
}

func usageDimensionValue(record storedUsageRecord, dimension string) string {
	switch dimension {
	case "router_model":
		if record.Attribution == attributionDirect || record.Attribution == attributionUnresolved {
			return record.Attribution
		}
		return record.RouterModel
	case "provider":
		return firstNonEmpty(record.Provider, "unknown")
	case "source":
		return firstNonEmpty(record.Source, "unknown")
	case "service_tier":
		return firstNonEmpty(record.ServiceTier, "default")
	case "result":
		return record.result()
	default:
		return record.ProviderModel
	}
}

func assignGroupDimension(group *usageGroup, record storedUsageRecord, dimension string) {
	switch dimension {
	case "router_model":
		group.RouterModel, group.Attribution = record.RouterModel, record.Attribution
	case "provider":
		group.Provider = record.Provider
	case "source":
		group.Source = record.Source
	case "service_tier":
		group.ServiceTier = record.ServiceTier
	case "result":
		group.Result = record.result()
	default:
		group.ProviderModel = record.ProviderModel
	}
}

func compareGroups(left, right usageGroup, field string) int {
	switch field {
	case "key":
		return strings.Compare(strings.ToLower(left.Key), strings.ToLower(right.Key))
	case "requests":
		return compareUint64(left.Requests, right.Requests)
	case "failed_requests":
		return compareUint64(left.FailedRequests, right.FailedRequests)
	case "input_tokens":
		return compareUint64(left.InputTokens, right.InputTokens)
	case "output_tokens":
		return compareUint64(left.OutputTokens, right.OutputTokens)
	case "latency":
		return compareUint64(left.AverageLatencyNS, right.AverageLatencyNS)
	case "ttft":
		return compareUint64(left.AverageTTFTNS, right.AverageTTFTNS)
	case "tps":
		return compareFloat(left.AverageTPS, right.AverageTPS)
	case "cost":
		return compareFloat(left.CostUSD, right.CostUSD)
	default:
		return compareUint64(left.TotalTokens, right.TotalTokens)
	}
}

func paginateGroups(items []usageGroup, offset, limit int) []usageGroup {
	if offset >= len(items) {
		return []usageGroup{}
	}
	end := offset + min(limit, len(items)-offset)
	return items[offset:end]
}

type usageRequestCandidate struct {
	storedUsageRecord
	cost               estimatedCost
	sortValue          float64
	generationNS       uint64
	tps                float64
	effectiveCache     uint64
	attempts           []usageRequestCandidate
	warning            string
	lifecycleRequestID string
	outcome            string
	requestedModel     string
	stream             bool
	aggregate          bool
}

// usageRequestSelection pages logical requests: exact trace parents, legacy router groups and standalone rows.
// It reads every record so filters and pagination never split a parent from its children.
type usageRequestSelection struct {
	options         usagePageOptions
	filter          usageFilter
	items           []usageRequestCandidate
	standalone      []usageRequestCandidate
	traces          map[string][]usageRequestCandidate
	legacy          map[string][]usageRequestCandidate
	sessions        map[string][]storedRequestSession
	total, capacity int
}

const ambiguousTraceWarning = "trace ID is shared by several inbound requests; execution records are not assigned to a parent"

func newUsageRequestSelection(options usagePageOptions, filter usageFilter) *usageRequestSelection {
	capacity := int(^uint(0) >> 1)
	if options.Offset <= capacity-options.Limit {
		capacity = options.Offset + options.Limit
	}
	return &usageRequestSelection{options: options, filter: filter, capacity: capacity, traces: make(map[string][]usageRequestCandidate), legacy: make(map[string][]usageRequestCandidate), sessions: make(map[string][]storedRequestSession)}
}

func (selection *usageRequestSelection) better(left, right usageRequestCandidate) bool {
	comparison := compareRequests(left, right, selection.options.Sort)
	if comparison == 0 {
		comparison = compareUint64(left.Sequence, right.Sequence)
	}
	if comparison == 0 {
		comparison = strings.Compare(left.TraceID, right.TraceID)
	}
	if comparison == 0 {
		comparison = strings.Compare(left.lifecycleRequestID, right.lifecycleRequestID)
	}
	if selection.options.Order == "asc" {
		return comparison < 0
	}
	return comparison > 0
}

func (selection *usageRequestSelection) Len() int { return len(selection.items) }
func (selection *usageRequestSelection) Less(i, j int) bool {
	return selection.better(selection.items[j], selection.items[i])
}
func (selection *usageRequestSelection) Swap(i, j int) {
	selection.items[i], selection.items[j] = selection.items[j], selection.items[i]
}
func (selection *usageRequestSelection) Push(value any) {
	selection.items = append(selection.items, value.(usageRequestCandidate))
}
func (selection *usageRequestSelection) Pop() any {
	last := len(selection.items) - 1
	value := selection.items[last]
	selection.items[last] = usageRequestCandidate{}
	selection.items = selection.items[:last]
	return value
}

func (selection *usageRequestSelection) add(record storedUsageRecord, cost estimatedCost) {
	candidate := usageRequestCandidate{storedUsageRecord: record, cost: cost}
	switch {
	case record.TraceID != "":
		selection.traces[record.TraceID] = append(selection.traces[record.TraceID], candidate)
	case record.Attribution == attributionRouted && record.RouterRequestID != "":
		selection.legacy[record.RouterRequestID] = append(selection.legacy[record.RouterRequestID], candidate)
	default:
		selection.standalone = append(selection.standalone, candidate)
	}
}

func (selection *usageRequestSelection) addSession(session storedRequestSession) {
	if session.TraceID != "" {
		selection.sessions[session.TraceID] = append(selection.sessions[session.TraceID], session)
	}
}

func (selection *usageRequestSelection) addLogical(candidate usageRequestCandidate) {
	if !selection.matchesLogical(candidate) {
		return
	}
	selection.total++
	if selection.capacity == 0 {
		return
	}
	if !candidate.aggregate {
		candidate.generationNS, candidate.tps = recordTiming(candidate.storedUsageRecord)
	}
	switch selection.options.Sort {
	case "cost":
		candidate.sortValue = candidate.cost.TotalUSD
	case "tps":
		candidate.sortValue = candidate.tps
	}
	if len(selection.items) < selection.capacity {
		selection.items = append(selection.items, candidate)
		if len(selection.items) == selection.capacity {
			heap.Init(selection)
		}
	} else if selection.better(candidate, selection.items[0]) {
		selection.items[0] = candidate
		heap.Fix(selection, 0)
	}
}

// matchesLogical applies parent-level filters to the parent and provider filters to any child, so a child match keeps the whole parent.
func (selection *usageRequestSelection) matchesLogical(candidate usageRequestCandidate) bool {
	filter := selection.filter
	if (!filter.From.IsZero() && candidate.RequestedAt.Before(filter.From)) || (!filter.To.IsZero() && !candidate.RequestedAt.Before(filter.To)) || (filter.Attribution != "" && !equalFold(candidate.Attribution, filter.Attribution)) || (filter.RouterModel != "" && !equalFold(candidate.RouterModel, filter.RouterModel)) || (filter.Result != "" && !equalFold(candidateResult(candidate), filter.Result)) {
		return false
	}
	if filter.ProviderModel == "" && filter.Source == "" && filter.ServiceTier == "" {
		return true
	}
	children := candidate.attempts
	if !candidate.aggregate {
		children = []usageRequestCandidate{candidate}
	}
	for _, child := range children {
		if (filter.ProviderModel == "" || equalFold(child.ProviderModel, filter.ProviderModel)) && (filter.Source == "" || equalFold(child.Source, filter.Source)) && (filter.ServiceTier == "" || equalFold(child.ServiceTier, filter.ServiceTier)) {
			return true
		}
	}
	return false
}

// candidateResult reports "unknown" for an exact-trace parent whose inbound completion has not been recorded.
func candidateResult(candidate usageRequestCandidate) string {
	if candidate.aggregate && candidate.TraceID != "" && candidate.outcome == "" {
		return "unknown"
	}
	return candidate.result()
}

func (selection *usageRequestSelection) finish(revision uint64, now time.Time) usageRequestPage {
	for _, candidate := range selection.standalone {
		selection.addLogical(candidate)
	}
	for trace, sessions := range selection.sessions {
		children := selection.traces[trace]
		delete(selection.traces, trace)
		if len(sessions) == 1 {
			selection.addLogical(requestParent(trace, &sessions[0], children))
			continue
		}
		// The short trace ID can collide; never guess which inbound request owns a delivered row.
		for i := range sessions {
			parent := requestParent(trace, &sessions[i], nil)
			parent.warning = ambiguousTraceWarning
			selection.addLogical(parent)
		}
		for _, child := range children {
			child.warning = ambiguousTraceWarning
			selection.addLogical(child)
		}
	}
	// Provider usage can arrive before lifecycle completion; its parent is derived from the rows and has an unknown result.
	for trace, children := range selection.traces {
		selection.addLogical(requestParent(trace, nil, children))
	}
	for id, attempts := range selection.legacy {
		sort.SliceStable(attempts, func(i, j int) bool {
			if attempts[i].RouterAttempt != attempts[j].RouterAttempt {
				return attempts[i].RouterAttempt < attempts[j].RouterAttempt
			}
			return attempts[i].Sequence < attempts[j].Sequence
		})
		parent := aggregateRequestAttempts(attempts)
		parent.RouterRequestID = id
		selection.addLogical(parent)
	}
	options := selection.options
	items := make([]usageRequestDetail, 0)
	if options.Offset < len(selection.items) {
		sort.Slice(selection.items, func(i, j int) bool { return selection.better(selection.items[i], selection.items[j]) })
		end := options.Offset + min(options.Limit, len(selection.items)-options.Offset)
		for _, candidate := range selection.items[options.Offset:end] {
			items = append(items, candidate.detail())
		}
	}
	return usageRequestPage{SchemaVersion: usageManagementSchemaVersion, GeneratedAt: now, PriceBookRevision: revision, Total: selection.total, Offset: options.Offset, Limit: options.Limit, Items: items}
}

func (candidate usageRequestCandidate) detail() usageRequestDetail {
	detail := requestDetailWithCost(candidate.storedUsageRecord, candidate.cost)
	detail.Result, detail.CorrelationWarning = candidateResult(candidate), candidate.warning
	detail.LifecycleRequestID, detail.Outcome, detail.RequestedModel, detail.Stream = candidate.lifecycleRequestID, candidate.outcome, candidate.requestedModel, candidate.stream
	if candidate.aggregate {
		detail.GenerationNS, detail.TPS = candidate.generationNS, candidate.tps
		detail.EffectiveCacheReadTokens, detail.CacheHit = candidate.effectiveCache, candidate.effectiveCache > 0
		detail.Attempts = make([]usageRequestDetail, 0, len(candidate.attempts))
		for _, child := range candidate.attempts {
			detail.Attempts = append(detail.Attempts, child.detail())
		}
		detail.AttemptCount = len(detail.Attempts)
	}
	return detail
}

// requestParent builds the logical parent of one inbound request, children ordered by start time then stored sequence.
// Matched-session route identity overrides stored heuristic attribution at presentation only; stored rows are untouched.
// Without a child the parent is lifecycle-only: no attempt is fabricated.
func requestParent(trace string, session *storedRequestSession, children []usageRequestCandidate) usageRequestCandidate {
	sort.SliceStable(children, func(i, j int) bool {
		if !children[i].RequestedAt.Equal(children[j].RequestedAt) {
			return children[i].RequestedAt.Before(children[j].RequestedAt)
		}
		return children[i].Sequence < children[j].Sequence
	})
	parent := usageRequestCandidate{aggregate: true}
	if len(children) > 0 {
		if session != nil {
			for i := range children {
				children[i].storedUsageRecord = sessionAttributedRecord(children[i].storedUsageRecord, *session)
			}
		}
		parent = aggregateRequestAttempts(children)
		parent.Provider, parent.ExecutorType, parent.ReasoningMode, parent.ProviderModel, parent.ProviderAlias, parent.ReasoningEffort, parent.ServiceTier, parent.MaskedAPIKey = "", "", "", "", "", "", "", ""
		parent.AuthID, parent.AuthIndex, parent.CredentialLabel, parent.Source, parent.AccountingMode = "", "", "", "", ""
		parent.Generate = false
		parent.RequestEndpoint = ""
		parent.Failed, parent.StatusCode = false, 0
	}
	parent.TraceID, parent.RequestID, parent.RouterRequestID, parent.RouterAttempt = trace, "", "", 0
	if session == nil {
		if len(children) == 0 {
			parent.Attribution = attributionDirect
		}
		return parent
	}
	parent.storedUsageRecord = sessionAttributedRecord(parent.storedUsageRecord, *session)
	parent.lifecycleRequestID, parent.outcome = session.LifecycleRequestID, session.Outcome
	parent.Failed = session.Outcome != "" && session.Outcome != "succeeded"
	parent.StatusCode = session.HTTPStatus
	if session.Latency > 0 {
		parent.LatencyNS = uint64(session.Latency)
	}
	parent.RequestEndpoint = firstNonEmpty(session.RequestEndpoint, parent.RequestEndpoint)
	parent.requestedModel, parent.stream = session.RequestedModel, session.Stream
	if !session.StartedAt.IsZero() {
		parent.RequestedAt = session.StartedAt
	}
	return parent
}

func sessionAttributedRecord(record storedUsageRecord, session storedRequestSession) storedUsageRecord {
	record.Attribution, record.RouterModel = attributionDirect, session.RouterModel
	if session.RouterModel != "" {
		record.Attribution = attributionRouted
	}
	return record
}

// aggregateRequestAttempts sums ordered delivered rows once; final-row identity is kept and earlier latency is added to TTFT.
func aggregateRequestAttempts(attempts []usageRequestCandidate) usageRequestCandidate {
	last := attempts[len(attempts)-1]
	parent := last
	parent.aggregate, parent.attempts, parent.warning = true, attempts, ""
	parent.InputTokens, parent.OutputTokens, parent.ReasoningTokens, parent.CachedTokens, parent.CacheReadTokens, parent.CacheCreationTokens, parent.TotalTokens = 0, 0, 0, 0, 0, 0, 0
	parent.LatencyNS, parent.TTFTNS, parent.generationNS, parent.effectiveCache = 0, 0, 0, 0
	parent.cost = estimatedCost{Priced: true, Source: last.cost.Source, AccountingMode: last.cost.AccountingMode, PriceServiceTier: last.cost.PriceServiceTier, TierThreshold: last.cost.TierThreshold}
	parent.RequestedAt, parent.Sequence = attempts[0].RequestedAt, attempts[0].Sequence
	for _, attempt := range attempts {
		parent.RequestedAt = earliestTime(parent.RequestedAt, attempt.RequestedAt)
		parent.Sequence = min(parent.Sequence, attempt.Sequence)
		parent.InputTokens += attempt.InputTokens
		parent.OutputTokens += attempt.OutputTokens
		parent.ReasoningTokens += attempt.ReasoningTokens
		parent.CachedTokens += attempt.CachedTokens
		parent.CacheReadTokens += attempt.CacheReadTokens
		parent.CacheCreationTokens += attempt.CacheCreationTokens
		parent.TotalTokens += attempt.TotalTokens
		parent.LatencyNS += attempt.LatencyNS
		parent.effectiveCache += effectiveCacheReadTokens(attempt.storedUsageRecord)
		generation, _ := recordTiming(attempt.storedUsageRecord)
		parent.generationNS += generation
		parent.cost.Priced = parent.cost.Priced && attempt.cost.Priced
		parent.cost.ContextTokens += attempt.cost.ContextTokens
		parent.cost.BillableInputTokens += attempt.cost.BillableInputTokens
		parent.cost.BilledCacheReadTokens += attempt.cost.BilledCacheReadTokens
		parent.cost.InputUSD += attempt.cost.InputUSD
		parent.cost.OutputUSD += attempt.cost.OutputUSD
		parent.cost.CacheReadUSD += attempt.cost.CacheReadUSD
		parent.cost.CacheCreationUSD += attempt.cost.CacheCreationUSD
		parent.cost.TotalUSD += attempt.cost.TotalUSD
	}
	if last.TTFTNS > 0 {
		parent.TTFTNS = last.TTFTNS + parent.LatencyNS - last.LatencyNS
	}
	if parent.generationNS > 0 {
		parent.tps = float64(parent.OutputTokens) / (float64(parent.generationNS) / float64(time.Second))
	}
	return parent
}

func earliestTime(left, right time.Time) time.Time {
	if right.Before(left) {
		return right
	}
	return left
}

func compareRequests(left, right usageRequestCandidate, field string) int {
	switch field {
	case "router_model":
		return strings.Compare(strings.ToLower(usageDimensionValue(left.storedUsageRecord, "router_model")), strings.ToLower(usageDimensionValue(right.storedUsageRecord, "router_model")))
	case "provider_model":
		return strings.Compare(strings.ToLower(left.ProviderModel), strings.ToLower(right.ProviderModel))
	case "source":
		return strings.Compare(strings.ToLower(left.Source), strings.ToLower(right.Source))
	case "service_tier":
		return strings.Compare(strings.ToLower(left.ServiceTier), strings.ToLower(right.ServiceTier))
	case "result":
		return strings.Compare(candidateResult(left), candidateResult(right))
	case "latency":
		return compareUint64(left.LatencyNS, right.LatencyNS)
	case "ttft":
		return compareUint64(left.TTFTNS, right.TTFTNS)
	case "tps":
		return compareFloat(left.sortValue, right.sortValue)
	case "total_tokens":
		return compareUint64(left.TotalTokens, right.TotalTokens)
	case "cost":
		return compareFloat(left.sortValue, right.sortValue)
	default:
		if left.RequestedAt.Before(right.RequestedAt) {
			return -1
		}
		if left.RequestedAt.After(right.RequestedAt) {
			return 1
		}
		return 0
	}
}

func compareUint64(left, right uint64) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func compareFloat(left, right float64) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func validateQueryChoice(value, fallback string, allowed []string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return fallback, nil
	}
	for _, candidate := range allowed {
		if value == candidate {
			return value, nil
		}
	}
	return "", fmt.Errorf("unsupported value %q", value)
}

type accountingDiagnosticKey struct {
	Model     string `json:"model"`
	Provider  string `json:"provider"`
	Executor  string `json:"executor"`
	Override  string `json:"price_override"`
	Stored    string `json:"stored_mode"`
	Default   string `json:"default_mode"`
	Effective string `json:"effective_mode"`
	Priced    bool   `json:"priced"`
}

type accountingDiagnosticTotals struct {
	Requests      uint64 `json:"requests"`
	Input         uint64 `json:"input_tokens"`
	CacheRead     uint64 `json:"cache_read_tokens"`
	CacheCreation uint64 `json:"cache_creation_tokens"`
	Included      uint64 `json:"cache_read_included_tokens"`
}

type usageAccountingDiagnostics struct {
	operation string
	started   time.Time
	filter    usageFilter
	groups    map[accountingDiagnosticKey]accountingDiagnosticTotals
}

func (diagnostics *usageAccountingDiagnostics) add(record storedUsageRecord, cost estimatedCost) {
	key := accountingDiagnosticKey{Model: record.ProviderModel, Provider: record.Provider, Executor: record.ExecutorType, Override: cost.priceAccountingMode, Stored: record.AccountingMode, Default: defaultAccountingMode(record.Provider, record.ExecutorType), Effective: effectiveAccountingMode(record, cost.AccountingMode), Priced: cost.Priced}
	totals := diagnostics.groups[key]
	totals.Requests++
	totals.Input += record.InputTokens
	totals.CacheRead += effectiveCacheReadTokens(record)
	totals.CacheCreation += record.CacheCreationTokens
	if key.Effective == accountingModeInputIncludesCache {
		totals.Included += effectiveCacheReadTokens(record)
	}
	diagnostics.groups[key] = totals
}

func (diagnostics *usageAccountingDiagnostics) finish(revision uint64, err error) {
	type decision struct {
		accountingDiagnosticKey
		accountingDiagnosticTotals
	}
	groups := make([]decision, 0, len(diagnostics.groups))
	for key, totals := range diagnostics.groups {
		groups = append(groups, decision{key, totals})
	}
	sort.Slice(groups, func(i, j int) bool {
		left, _ := json.Marshal(groups[i])
		right, _ := json.Marshal(groups[j])
		return string(left) < string(right)
	})
	filter := diagnostics.filter
	source := filter.Source
	if looksLikeCredential(source) {
		source = "[omitted: credential-shaped source filter]"
	}
	fields := map[string]any{"operation_id": diagnostics.operation, "price_book_revision": revision, "from": filter.From, "to": filter.To, "attribution": diagnosticFilterValue(filter.Attribution), "router_model": diagnosticFilterValue(filter.RouterModel), "provider_model": diagnosticFilterValue(filter.ProviderModel), "source": diagnosticFilterValue(source), "service_tier": diagnosticFilterValue(filter.ServiceTier), "result": diagnosticFilterValue(filter.Result), "groups": groups}
	logModelRouterJSON(diagnostics.operation, "model_router.accounting.decisions", fields)
	outcome := diagnosticErrorFields(err)
	outcome["operation_id"] = diagnostics.operation
	outcome["price_book_revision"] = revision
	outcome["elapsed_ms"] = time.Since(diagnostics.started).Milliseconds()
	outcome["decision_groups"] = len(groups)
	if err != nil {
		outcome["outcome"] = "failed"
	} else {
		outcome["outcome"] = "complete"
	}
	logModelRouterEvent("", "debug", "model_router.query.complete", outcome)
}
