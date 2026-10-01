# Contributing

Issues and pull requests are welcome.

## Before sending a change

```sh
make        # lint (gofmt, go mod tidy, vet, golangci-lint, actionlint), tests with -race, static build
make cover  # must stay at or above 90%
```

Go 1.27.1 or later is required. If you touch a parser, run `make fuzz` for a while; if you touch device I/O, run `make integration` as root on a test machine.

- Keep the fail-closed policy. A change must never make a drive that was not cryptographically erased end up as `PASS`.
- New device behaviour needs a test. The fakes in `fakes_test.go` simulate whole servers; add a scenario to `run_test.go` or `edge_test.go`. Timing rules belong in `synctest_test.go`, which runs on a fake clock.
- If you change the report, regenerate the examples with `make examples` and update `docs/report.md`.

## Signed commits

Every commit on `main` should show as Verified on GitHub.

- Changes reach `main` through pull requests merged with **Squash and merge**, which GitHub signs. Merge commits and rebase merges are disabled, because they would bring unsigned branch commits onto `main`.
- If you push directly, sign your commits. With an SSH key:

  ```sh
  git config --global gpg.format ssh
  git config --global user.signingkey ~/.ssh/id_ed25519.pub
  git config --global commit.gpgsign true
  ```

  Add the same public key on GitHub under Settings → SSH and GPG keys as a **Signing Key**, and commit with an email address that is verified on your account (the `@users.noreply.github.com` address works).

## Hardware reports

Results from real drives are the most useful contribution. Please open an issue that includes:

- drive model, firmware, and the controller or HBA it is attached to;
- the output of `cryptoerase --inventory --report inv.json`, with serial numbers redacted if you prefer;
- the result of a test erase on a spare drive, if you ran one.

## Firmware floor rules

A pull request that adds a rule to `DefaultFirmwarePolicyText` should link a public vendor advisory that covers the affected firmware versions.
