package install

import (
	"bytes"
	"debug/elf"
	"debug/macho"
	"encoding/binary"
	"errors"
	"fmt"
)

// Binary hardening probes for doctor check tier.binary_hardening
// (docs/p2-design.md §3.5, v1.4). Both functions parse bytes with the
// standard library only (no exec, no network) so they run on every host and
// are tested with synthetic fixtures.

const (
	lcCodeSignature   = 0x1d       // LC_CODE_SIGNATURE
	csMagicEmbedded   = 0xfade0cc0 // CSMAGIC_EMBEDDED_SIGNATURE (SuperBlob)
	csMagicCodeDir    = 0xfade0c02 // CSMAGIC_CODEDIRECTORY
	csSlotCodeDir     = 0          // CSSLOT_CODEDIRECTORY
	csRuntime         = 0x00010000 // CS_RUNTIME (hardened runtime)
	csDirFlagsOffset  = 12         // magic, length, version, flags
	machoMaxSignature = 64 << 20
)

// machoState is the hardened-runtime state of one Mach-O image.
type machoState int

const (
	machoUnsigned machoState = iota // no LC_CODE_SIGNATURE (or no CodeDirectory)
	machoSignedNoRuntime
	machoHardened
)

// machoRuntime inspects a Mach-O (thin or universal) and returns the weakest
// state over all architecture slices: hardened only when EVERY slice carries
// a CodeDirectory with CS_RUNTIME. Without CS_RUNTIME the dynamic loader
// honours DYLD_INSERT_LIBRARIES from the environment (a project-layer
// settings "env" reaches the managed hook), so the binary can be made to load
// agent-chosen code.
func machoRuntime(b []byte) (machoState, error) {
	if ff, err := macho.NewFatFile(bytes.NewReader(b)); err == nil {
		defer ff.Close()
		if len(ff.Arches) == 0 {
			return machoUnsigned, errors.New("universal binary without architectures")
		}
		worst := machoHardened
		for _, a := range ff.Arches {
			lo, hi := int64(a.Offset), int64(a.Offset)+int64(a.Size)
			if lo < 0 || hi > int64(len(b)) || lo > hi {
				return machoUnsigned, errors.New("universal binary slice out of range")
			}
			st, err := thinMachoRuntime(b[lo:hi])
			if err != nil {
				return machoUnsigned, err
			}
			if st < worst {
				worst = st
			}
		}
		return worst, nil
	} else if !errors.Is(err, macho.ErrNotFat) {
		return machoUnsigned, err
	}
	return thinMachoRuntime(b)
}

func thinMachoRuntime(b []byte) (machoState, error) {
	f, err := macho.NewFile(bytes.NewReader(b))
	if err != nil {
		return machoUnsigned, err
	}
	defer f.Close()
	for _, l := range f.Loads {
		raw := l.Raw()
		if len(raw) < 16 || f.ByteOrder.Uint32(raw[0:4]) != lcCodeSignature {
			continue
		}
		off, size := uint64(f.ByteOrder.Uint32(raw[8:12])), uint64(f.ByteOrder.Uint32(raw[12:16]))
		if size == 0 || size > machoMaxSignature || off+size > uint64(len(b)) {
			return machoUnsigned, errors.New("code signature outside the file")
		}
		return codeDirectoryState(b[off : off+size])
	}
	return machoUnsigned, nil
}

// codeDirectoryState reads the primary CodeDirectory of an embedded
// signature SuperBlob (all fields big-endian).
func codeDirectoryState(sig []byte) (machoState, error) {
	be := binary.BigEndian
	if len(sig) < 12 || be.Uint32(sig[0:4]) != csMagicEmbedded {
		return machoUnsigned, errors.New("code signature is not an embedded SuperBlob")
	}
	count := be.Uint32(sig[8:12])
	if uint64(12)+uint64(count)*8 > uint64(len(sig)) {
		return machoUnsigned, errors.New("code signature index truncated")
	}
	for i := uint32(0); i < count; i++ {
		ent := sig[12+8*i : 12+8*i+8]
		if be.Uint32(ent[0:4]) != csSlotCodeDir {
			continue
		}
		off := uint64(be.Uint32(ent[4:8]))
		if off+csDirFlagsOffset+4 > uint64(len(sig)) || be.Uint32(sig[off:off+4]) != csMagicCodeDir {
			return machoUnsigned, errors.New("CodeDirectory truncated")
		}
		if be.Uint32(sig[off+csDirFlagsOffset:off+csDirFlagsOffset+4])&csRuntime != 0 {
			return machoHardened, nil
		}
		return machoSignedNoRuntime, nil
	}
	return machoUnsigned, nil
}

// elfDynamic reports whether an ELF image has a PT_INTERP program header
// (dynamically linked). A dynamic binary honours LD_PRELOAD from the
// environment of the managed hook.
func elfDynamic(b []byte) (bool, error) {
	f, err := elf.NewFile(bytes.NewReader(b))
	if err != nil {
		return false, err
	}
	defer f.Close()
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			return true, nil
		}
	}
	return false, nil
}

func describeMacho(st machoState) string {
	switch st {
	case machoHardened:
		return "hardened runtime (CS_RUNTIME)"
	case machoSignedNoRuntime:
		return fmt.Sprintf("signed without hardened runtime (CodeDirectory flags lack 0x%x)", csRuntime)
	default:
		return "not code-signed"
	}
}
