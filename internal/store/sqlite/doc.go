// Package sqlite is the robot's single storage backend: it opens the one
// SQLite database file, applies embedded migrations, and exposes a
// concrete repository per aggregate (DESIGN.md §5). There is no generic
// DAO/ORM layer — each repository is plain SQL over the narrow set of
// queries its Phase-1 caller needs; a repository method takes a Querier so
// callers can compose several mutations into one transaction via WithTx.
package sqlite
