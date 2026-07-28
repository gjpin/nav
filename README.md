# Navigator

`navigator [directory]` is a read-only, Git-aware terminal file explorer for
narrow terminals. It includes ignored files, omits `.git`, and never follows
directory symlinks.

Navigator opens large repositories without recursively indexing them first.
It loads each directory only when expanded; very large flat directories appear
in batches and are alphabetized when loading finishes. Git badges populate
after the explorer opens, with untracked entries resolved as their directories
are loaded.

Run it with Go 1.26:

```sh
go run . [directory]
```

Use the arrow keys (or `j`/`k`) to move in the explorer, `Enter` to expand or
open, and `Tab` to focus the preview. `d` switches between a full-file preview
and a unified diff. `/` searches the current preview; `n`/`N` navigate matches.
`r` refreshes, `y` copies a selected preview range, and `q` quits.

The explorer marks changed files yellow, added or untracked files green, and
deleted paths red. Deleted Git paths remain visible as red ghost entries.
