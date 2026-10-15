package install

import (
	"sort"

	"github.com/libingtong/ballast/internal/model"
)

// nonNilManifest returns m, or an empty manifest when m is nil.
func nonNilManifest(m *model.InstallManifest) *model.InstallManifest {
	if m == nil {
		return &model.InstallManifest{V: model.ManifestVersion}
	}
	return m
}

func entriesFor(m *model.InstallManifest, c model.ClientID, file string) []model.ManagedEntry {
	var out []model.ManagedEntry
	if m == nil {
		return nil
	}
	for _, e := range m.Entries {
		if e.Client == c && e.File == file {
			out = append(out, e)
		}
	}
	return out
}

func fileRec(m *model.InstallManifest, path string) *model.ManagedFile {
	if m == nil {
		return nil
	}
	for i := range m.Files {
		if m.Files[i].Path == path {
			return &m.Files[i]
		}
	}
	return nil
}

func flagsFor(m *model.InstallManifest, c model.ClientID, file string) []model.ManagedFlag {
	var out []model.ManagedFlag
	if m == nil {
		return nil
	}
	for _, f := range m.Flags {
		if f.Client == c && f.File == file {
			out = append(out, f)
		}
	}
	return out
}

func clientIndex(c model.ClientID) int {
	for i, x := range model.AllClients {
		if x == c {
			return i
		}
	}
	return len(model.AllClients)
}

func nativeIndex(c model.ClientID, native string) int {
	for i, ne := range model.NativeEventTable[c] {
		if ne.Native == native {
			return i
		}
	}
	return 1 << 20
}

// sortManifest orders every record list deterministically so that repeated
// installs produce byte-identical manifests.
func sortManifest(m *model.InstallManifest) {
	sort.SliceStable(m.Entries, func(i, j int) bool {
		a, b := m.Entries[i], m.Entries[j]
		if ca, cb := clientIndex(a.Client), clientIndex(b.Client); ca != cb {
			return ca < cb
		}
		if a.File != b.File {
			return a.File < b.File
		}
		if na, nb := nativeIndex(a.Client, a.Native), nativeIndex(b.Client, b.Native); na != nb {
			return na < nb
		}
		return a.Key < b.Key
	})
	sort.SliceStable(m.Files, func(i, j int) bool {
		a, b := m.Files[i], m.Files[j]
		if ca, cb := clientIndex(a.Client), clientIndex(b.Client); ca != cb {
			return ca < cb
		}
		return a.Path < b.Path
	})
	sort.SliceStable(m.Flags, func(i, j int) bool {
		a, b := m.Flags[i], m.Flags[j]
		if ca, cb := clientIndex(a.Client), clientIndex(b.Client); ca != cb {
			return ca < cb
		}
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Key < b.Key
	})
}

// defaultEvents returns the NativeEventTable entries with Install == true.
func defaultEvents(c model.ClientID) []model.NativeEvent {
	var out []model.NativeEvent
	for _, ne := range model.NativeEventTable[c] {
		if ne.Install {
			out = append(out, ne)
		}
	}
	return out
}
