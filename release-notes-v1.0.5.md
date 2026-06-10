# v1.0.5 Release Notes

## Summary

- Switches the module path and install/update sources to `github.com/lighttous/ppanel-node`.
- Hardens startup, shutdown, and reload behavior to reduce service interruption risk.
- Fixes user reconciliation and online user reporting correctness.
- Adds targeted regression tests for reload, limiter refresh, and user diff logic.

## Included Changes

- Module path migration
  - Updates `go.mod`, internal imports, and `proto` `go_package`.
  - Aligns install and release scripts with the `lighttous/ppanel-node` repository.

- Runtime lifecycle hardening
  - Avoids nil log writer crashes when log files cannot be opened.
  - Makes `Close()` paths idempotent for `XrayCore`, `Node`, and `Controller`.
  - Improves reload rollback behavior when old runtime shutdown reports errors.

- User sync correctness
  - Detects updated users by `uuid` and applies `SpeedLimit` and `DeviceLimit` changes.
  - Refreshes limiter caches when user limits are updated.
  - Reports only online users that produced traffic in the current reporting window.

## Release Checklist

- [ ] `GOEXPERIMENT=jsonv2 go test ./...`
- [ ] Verify `scripts/install.sh` can perform a fresh install from the new repo path.
- [ ] Verify `ppnode update v1.0.5` works on an existing node.
- [ ] Verify config change triggers reload and service keeps running.
- [ ] Verify GitHub release assets are produced for all target architectures.
- [ ] Confirm `README.md` install and publish examples match `v1.0.5`.

## Suggested Release Command

```bash
./scripts/publish-new-repo-release.sh v1.0.5
```
