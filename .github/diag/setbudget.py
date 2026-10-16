import sys
p = 'internal/state/store.go'
s = open(p).read()
old = 'LockBudget        = 20 * time.Millisecond'
assert old in s
open(p, 'w').write(s.replace(old, 'LockBudget        = %s * time.Millisecond' % sys.argv[1]))
