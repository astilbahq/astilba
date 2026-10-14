# Astilba apex site

Read `../docs/AGENTS.md` and `../docs/PRODUCT.md`. This app owns the apex origin and the exact `/docs` redirect. The docs Worker owns `/docs/*`. Preserve independent build artifacts. Consume workspace UI/tokens while keeping old-repository deployment pins unchanged.

Run root `pnpm verify:web`. Shared public logomarks and generated build configuration remain in `../docs`. Production deployment remains in the original Docs repository.
