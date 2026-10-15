package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	lockBudget = 20 * time.Millisecond // B6:最长等待 20ms
	seenCap    = 512
	ringCap    = 8
)

type RingEv struct {
	Seq  int    `json:"seq"`
	Ev   string `json:"ev"`
	Tool string `json:"tool,omitempty"`
	Sum  string `json:"sum,omitempty"`
}

type State struct {
	V          int            `json:"v"`
	NEvents    int            `json:"n_events"`
	ToolCount  int            `json:"tool_count"`
	Seen       []string       `json:"seen"`
	Read       []string       `json:"read"`
	Ring       []RingEv       `json:"ring"`
	PriorHit   map[string]int `json:"prior_hit"`
	LastPrompt string         `json:"last_prompt"`
	Gen        string         `json:"gen"`
}

// Delta 是一次事务要合并进状态的增量。
type Delta struct {
	ToolUseID  string
	ReadPaths  []string
	Hits       []string
	Ev         RingEv
	LastPrompt string
	IsTool     bool
	Gen        string
}

func newState() *State { return &State{V: 1, PriorHit: map[string]int{}} }

// readState 读取状态;文件不存在返回空状态;损坏(无法解析)返回空状态且 corrupt=true。
func readState(path string) (st *State, corrupt bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return newState(), false
	}
	st = newState()
	if err := json.Unmarshal(b, st); err != nil || st.V != 1 {
		return newState(), true
	}
	if st.PriorHit == nil {
		st.PriorHit = map[string]int{}
	}
	return st, false
}

func sanitize(s string) string {
	ok := s != "" && len(s) <= 64
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			ok = false
			break
		}
	}
	if ok {
		return s
	}
	h := sha256.Sum256([]byte(s))
	return "h" + hex.EncodeToString(h[:8])
}

func repoInst(cwd string) string {
	h := sha256.Sum256([]byte(cwd))
	return hex.EncodeToString(h[:6])
}

func statePath(home, cwd, session, agent string) string {
	if agent == "" {
		agent = "main"
	}
	return filepath.Join(home, "state", repoInst(cwd), sanitize(session), sanitize(agent)+".json")
}

type txResult struct {
	Status   string // ok | lock_timeout
	WaitUS   int64
	Corrupt  bool
	NEvents  int
	Attempts int
}

// commitState 在 flock 之下完成"读-改-写"事务:
// 非阻塞 flock 重试至多 20ms,超时则跳过状态写入(只返回诊断);
// 成功则临时文件 + rename 原子替换。
func commitState(path string, d *Delta, doFsync bool) (txResult, error) {
	var res txResult
	lockPath := path + ".lock"
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if errors.Is(err, os.ErrNotExist) {
		if err = os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
			lf, err = os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
		}
	}
	if err != nil {
		return res, err
	}
	defer lf.Close() // 关闭即释放 flock

	start := time.Now()
	for {
		res.Attempts++
		err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EINTR {
			return res, err
		}
		if time.Since(start) >= lockBudget {
			res.Status = "lock_timeout"
			res.WaitUS = time.Since(start).Microseconds()
			return res, nil
		}
		time.Sleep(200 * time.Microsecond)
	}
	res.WaitUS = time.Since(start).Microseconds()

	st, corrupt := readState(path)
	res.Corrupt = corrupt
	applyDelta(st, d)
	b, err := json.Marshal(st)
	if err != nil {
		return res, err
	}
	if err := atomicWrite(path, b, doFsync); err != nil {
		return res, err
	}
	res.Status = "ok"
	res.NEvents = st.NEvents
	return res, nil
}

func applyDelta(st *State, d *Delta) {
	for _, id := range st.Seen {
		if id != "" && id == d.ToolUseID {
			return // 重复投递:不重复记账
		}
	}
	st.NEvents++
	d.Ev.Seq = st.NEvents
	if d.ToolUseID != "" {
		st.Seen = append(st.Seen, d.ToolUseID)
		if len(st.Seen) > seenCap {
			st.Seen = st.Seen[len(st.Seen)-seenCap:]
		}
	}
	if d.IsTool {
		st.ToolCount++
	}
	for _, p := range d.ReadPaths {
		found := false
		for _, q := range st.Read {
			if q == p {
				found = true
				break
			}
		}
		if !found {
			st.Read = append(st.Read, p)
		}
	}
	for _, r := range d.Hits {
		st.PriorHit[r]++
	}
	st.Ring = append(st.Ring, d.Ev)
	if len(st.Ring) > ringCap {
		st.Ring = st.Ring[len(st.Ring)-ringCap:]
	}
	if d.LastPrompt != "" {
		st.LastPrompt = d.LastPrompt
	}
	if d.Gen != "" {
		st.Gen = d.Gen
	}
}

// atomicWrite:写同目录临时文件再 rename。fsync 默认关闭(见 M0c-PERF 的说明)。
func atomicWrite(path string, data []byte, doFsync bool) error {
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if doFsync {
		if err := f.Sync(); err != nil {
			f.Close()
			os.Remove(tmp)
			return err
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[len(s)-n:], "")
}
