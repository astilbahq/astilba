# HTTP contract

`openapi.yaml` is authoritative for browser and agent request/response structures. Regenerate with `pnpm generate` and verify with `pnpm generate:check`. Keep field names and successful status codes aligned with handlers. Credential and secret fields are write-only; database rows are not API DTOs. Add strict runtime validation and domain checks where a property relation cannot be expressed by the schema. Keep agent routes under `/agent/v1` with scoped bearer authentication, separate from browser cookie/CSRF routes.
