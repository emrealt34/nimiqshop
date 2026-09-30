package nimiq

import (
	"bufio"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// vectors loads testdata/vectors.txt ("name = value" lines, "#" comments).
func vectors(t testing.TB) map[string]string {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "vectors.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("testdata/vectors.txt: malformed line %q", line)
		}
		out[strings.TrimSpace(name)] = strings.TrimSpace(value)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// hexVector decodes the named vector, failing the test if it is missing.
func hexVector(t testing.TB, v map[string]string, name string) []byte {
	t.Helper()
	raw, ok := v[name]
	if !ok {
		t.Fatalf("testdata/vectors.txt: missing %q", name)
	}
	b, err := hex.DecodeString(raw)
	if err != nil {
		t.Fatalf("testdata/vectors.txt: %s is not hex: %v", name, err)
	}
	return b
}

// Official BIP39 test vectors (Trezor python-mnemonic vectors.json, English,
// empty passphrase).
func TestBIP39SeedVectors(t *testing.T) {
	v := vectors(t)
	for _, words := range []string{"24", "12"} {
		mn, seed := v["bip39."+words+".mnemonic"], v["bip39."+words+".seed"]
		got := pbkdf2SHA512([]byte(mn), []byte("mnemonic"), 2048, 64)
		if hex.EncodeToString(got) != seed {
			t.Fatalf("BIP39 seed mismatch for %d-word phrase:\n got %x\nwant %s", len(strings.Fields(mn)), got, seed)
		}
	}
}

// Official SLIP-0010 ed25519 test vector 1 (seed 000102…0f).
func TestSLIP10Vectors(t *testing.T) {
	v := vectors(t)
	key, chain := slip10MasterKey(hexVector(t, v, "slip10.seed"))
	if hex.EncodeToString(key) != v["slip10.m.private"] {
		t.Fatalf("SLIP-10 master key mismatch: %x", key)
	}
	if hex.EncodeToString(chain) != v["slip10.m.chain"] {
		t.Fatalf("SLIP-10 master chain mismatch: %x", chain)
	}
	// Chain m/0H (hardened):
	k0, c0 := slip10ChildKey(key, chain, 0x80000000)
	if hex.EncodeToString(k0) != v["slip10.m0h.private"] {
		t.Fatalf("SLIP-10 child m/0' key mismatch: %x", k0)
	}
	if hex.EncodeToString(c0) != v["slip10.m0h.chain"] {
		t.Fatalf("SLIP-10 child m/0' chain mismatch: %x", c0)
	}
}

func TestSeedFromMnemonicDeterministic(t *testing.T) {
	vec24Mnemonic := vectors(t)["bip39.24.mnemonic"]
	a, err := SeedFromMnemonic(vec24Mnemonic, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Messy paste: extra spaces, commas, wrong case — must still work.
	b, err := SeedFromMnemonic("  Abandon abandon,  abandon "+strings.Repeat("abandon ", 20)+"ART", 0)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("derivation not deterministic:\n %s\n %s", a, b)
	}
	if len(a) != 64 {
		t.Fatalf("seed must be 32-byte hex, got %d chars", len(a))
	}
	// A different account index must yield a different key.
	c, err := SeedFromMnemonic(vec24Mnemonic, 1)
	if err != nil {
		t.Fatal(err)
	}
	if a == c {
		t.Fatal("account 0 and account 1 derived the same key")
	}
}

func TestMnemonicChecksumRejection(t *testing.T) {
	// Valid phrase, corrupted last word — checksum must fail.
	bad := strings.Repeat("abandon ", 23) + "abandon"
	if _, err := SeedFromMnemonic(bad, 0); err == nil {
		t.Fatal("corrupted phrase must be rejected by the BIP39 checksum")
	}
	// Word order swap.
	swapped := "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon art abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon"
	if _, err := SeedFromMnemonic(swapped, 0); err == nil {
		t.Fatal("swapped words must be rejected by the BIP39 checksum")
	}
	// Unknown word.
	if _, err := SeedFromMnemonic("xylophonez "+strings.Repeat("abandon ", 23), 0); err == nil {
		t.Fatal("unknown word must be rejected")
	}
	// Wrong word count.
	if _, err := SeedFromMnemonic("abandon about", 0); err == nil {
		t.Fatal("wrong word count must be rejected")
	}
}

func TestResolveWalletSecret(t *testing.T) {
	v := vectors(t)
	vec24Mnemonic, vec24Seed := v["bip39.24.mnemonic"], v["bip39.24.seed"]
	// 32-byte hex passes through untouched.
	hexSeed := vec24Seed[:64]
	out, derived, err := ResolveWalletSecret(hexSeed, 0)
	if err != nil || derived || out != hexSeed {
		t.Fatalf("hex passthrough failed: out=%s derived=%v err=%v", out, derived, err)
	}
	// 64-byte hex passes through too.
	long := vec24Seed // 64-byte key = 128 hex chars
	out2, derived2, err2 := ResolveWalletSecret(long, 0)
	if err2 != nil || derived2 || out2 != long {
		t.Fatalf("64-byte hex passthrough failed: derived=%v err=%v", derived2, err2)
	}
	// Mnemonic resolves to a seed and reports derived=true; seed matches the
	// direct derivation.
	want, err := SeedFromMnemonic(vec24Mnemonic, 0)
	if err != nil {
		t.Fatal(err)
	}
	out3, derived3, err3 := ResolveWalletSecret(vec24Mnemonic, 0)
	if err3 != nil || !derived3 || out3 != want {
		t.Fatalf("mnemonic resolution failed: out=%s derived=%v err=%v", out3, derived3, err3)
	}
	// Address derivable, well-formed and stable across calls.
	addr1, err := AddressFromMnemonic(vec24Mnemonic, 0)
	if err != nil || !strings.HasPrefix(addr1, "NQ") {
		t.Fatalf("address derivation failed: %v %q", err, addr1)
	}
	if ValidateAddress(addr1) != nil {
		t.Fatalf("derived address failed checksum: %q", addr1)
	}
	addr2, _ := AddressFromMnemonic(vec24Mnemonic, 0)
	if addr1 != addr2 {
		t.Fatalf("address not deterministic: %q vs %q", addr1, addr2)
	}
	// Different account → different address.
	addrOther, _ := AddressFromMnemonic(vec24Mnemonic, 1)
	if addrOther == addr1 {
		t.Fatal("account 1 derived the same address as account 0")
	}
}
