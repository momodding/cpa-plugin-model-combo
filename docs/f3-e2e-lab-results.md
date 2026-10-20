# F3 Disposable Docker E2E Lab Results

## Scope

This report records observed results from disposable Docker E2E lab work. It records no result that was not observed. The lab was not rerun after its final timeout.

## Commands and observed results

| Command or operation | Observed result |
|---|---|
| Initial pull of `docker.io/library/golang:1.26` | Timed out at 600 seconds. A later pull completed. |
| `sh -n tests/e2e/lab/lab.sh` | Passed. |
| Docker lab build with repository mounted read-only | Go VCS stamping failed. |
| Docker-lab-only repair | Build command used `-buildvcs=false`; this repaired the VCS-stamping failure for the lab build path. |
| Bounded lab startup through `tests/e2e/lab/lab.sh up` | Timed out at 600.25 seconds during Go dependency downloads, including CLIProxyAPI v7.3.9 and `go-sqlite3`. |

The lab script's build command is:

```sh
docker run --rm \
  -v "$repo_dir:/src:ro" \
  -v "$workspace/plugins:/out" \
  -w /src \
  "${CPA_E2E_GO_IMAGE:-docker.io/library/golang:1.26}" \
  sh -c 'CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -buildvcs=false -trimpath -buildmode=c-shared -o /out/model-combo.so .'
```

The lab script's bounded startup path is `tests/e2e/lab/lab.sh up`, guarded by `CPA_E2E_ALLOW_START=1`. Its intended Compose start operation is:

```sh
docker compose -p "$project" -f "$compose_file" --project-directory "$workspace" up -d
```

## Boundary status

| Boundary | Status | Evidence limitation |
|---|---|---|
| Go image availability | Partial | Initial pull timed out; later pull completed. |
| Lab script syntax | Observed pass | `sh -n` passed. |
| Read-only source build | Repaired in lab path | Go VCS stamping failed before `-buildvcs=false`; repair was Docker-lab-only. |
| Final bounded startup | Timed out | Timeout occurred during dependency downloads, including CLIProxyAPI v7.3.9 and `go-sqlite3`. |
| Compose startup | Not observed | Compose never ran. |
| CPA container | Not observed | No CPA container was observed. |
| CPA process | Not observed | No CPA process was observed. |
| CPA readiness or health | Not observed | No readiness or health result was observed. |
| Browser E2E | Not observed | No browser E2E ran. |
| Cleanup | Not completed | Workspaces were retained for evidence. |

No container ID, process ID, readiness result, health result, browser result, or exit code is asserted here.

## Limitations

- Final bounded startup stopped at 600.25 seconds while Go dependencies downloaded.
- Compose never ran, so no CPA runtime behavior was exercised.
- No CPA container or process reached a state where readiness or health could be checked.
- Browser E2E was not reached.
- Retained workspaces remain available for inspection.
- No retry occurred after final timeout.
- This report contains no raw API keys or management keys.

## Next bounded actions

1. Inspect retained workspaces and dependency-download logs without rerunning the lab.
2. If another run is authorized, set a bounded dependency-cache or network plan before invoking `CPA_E2E_ALLOW_START=1 tests/e2e/lab/lab.sh up`.
3. Record whether Compose starts, then separately record CPA readiness, health, and browser E2E results.
4. Run lab cleanup only against a retained workspace using the path printed by the lab, after evidence capture.
