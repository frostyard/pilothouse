# Repository policy

[`repository.yaml`](repository.yaml) is the machine-readable source of truth
for Pilothouse's structural governance requirements. `required_files` lists
individual assets that must remain regular files. Each `required_globs` entry
sets a repository-relative pattern and the minimum number of regular files it
must match.

The policy is enforced by `TestRepositoryPolicy` in
`internal/workflowcheck/repository_policy_test.go`. Run it directly with:

```sh
go test ./internal/workflowcheck -run RepositoryPolicy
```

The same test runs in the normal `make test` and `make ci` paths, so pull
requests and nightly compliance fail if required governance assets disappear
or the policy document is invalid.
