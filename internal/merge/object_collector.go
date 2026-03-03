package merge

// objectCollector is no longer needed — native git commands (git hash-object,
// git mktree, git commit-tree) write objects directly to the store.
// This file is intentionally left as a placeholder to avoid breaking test
// imports during the migration. It will be removed once all tests are updated.
