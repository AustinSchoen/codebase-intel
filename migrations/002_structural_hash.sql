-- Add structural_hash column to file_state for AST-aware summary invalidation.
-- This hash ignores comments and whitespace so that non-semantic changes
-- (e.g., adding a comment) do not trigger summary regeneration.
ALTER TABLE file_state ADD COLUMN IF NOT EXISTS structural_hash TEXT;
