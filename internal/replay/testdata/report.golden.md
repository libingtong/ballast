# Ballast replay report

- Transcript: `~/.claude/projects/p/s.jsonl`
- Client: claude-code
- Rules hash: `abc123`
- Items: 120 (skipped lines: 2)

## Summary

| rule | known-hit | known-miss | approximate | unknown | labeled | precision |
|---|---:|---:|---:|---:|---:|---:|
| read-before-edit | 4 | 90 | 0 | 1 | 4 | 75.0% |
| claims-done | 0 | 0 | 3 | 0 | 0 | - |
| quiet-rule | 0 | 7 | 0 | 0 | 0 | - |

## Samples

### read-before-edit

| id | class | event | ts | uuid | detail | label |
|---|---|---|---|---|---|---|
| `0123456789abcdef` | known-hit | PreToolUse | 2026-10-10T09:00:04Z | `u004` | path: /repo/a.go \| b | tp |
| `fedcba9876543210` | unknown | PreToolUse | - | `line-9` | dynamic argument | - |

### claims-done

| id | class | event | ts | uuid | detail | label |
|---|---|---|---|---|---|---|
| `1111222233334444` | approximate | Stop | 2026-10-10T09:05:00Z | `u050#1` | all done | - |

