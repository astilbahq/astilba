# Contributing

Read the [product scope](docs/platform-product-brief.md) and [engineering principles](docs/engineering-principles.md). Keep a pull request focused on one observable outcome and explain its validation and any material limitations.

Run `make verify` with a disposable local Docker engine before submitting code. The [experiment README](experiments/durable-execution/README.md) provides pinned images and setup instructions. Unit tests should protect load-bearing behavior; integration tests should prove guarantees that depend on databases or containers.

Keep credentials, private URLs, deployment identifiers and personal information out of public changes and test fixtures. Report vulnerabilities through the process in [SECURITY.md](SECURITY.md).

Contributions are licensed under AGPL-3.0-only. Contributors retain copyright and do not grant a special commercial relicensing right. Sign off each commit to certify the [Developer Certificate of Origin](https://developercertificate.org/):

```sh
git commit --signoff
```

Use a GitHub noreply address if you do not want a personal email address in public commit metadata. Agent-assisted changes remain the submitter's responsibility.
