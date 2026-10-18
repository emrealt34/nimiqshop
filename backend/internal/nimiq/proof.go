package nimiq

/* proof.go — who signed an on-chain transaction.

The RPC reports a transaction's `from` as the account that was debited. For a
basic account that is the owner's address. For a contract it is the contract's
address, and the keys that authorised the spend are only visible inside the
transaction proof.

Nimiq Pay keeps a user's NIM in a per-user HTLC (sender = the user's own key,
recipient = a Nimiq Pay co-signing key, hash root all zeros, ~2 week timeout)
and pays from it with an EarlyResolve proof that carries BOTH signatures. So a
Nimiq Pay payment shows `from` = the HTLC address and `fromType` = 2, while the
buyer's address (the one they signed in with) is in the proof, not in `from`.
ProofSigners recovers those addresses so a verifier can match the payer by the
key that actually signed.

Wire format (nimiq-serde / postcard, verified against mainnet transactions):

	SignatureProof  = type_and_flags u8          low nibble: 0 Ed25519, 1 ES256
	                                             high nibble: bit0 = webauthn fields follow
	                  public_key      32 | 33 bytes
	                  merkle_path     count u8 [, left_bits (varint len + bytes), hashes (varint len + 32 bytes each)]
	                  signature       64 bytes
	                  [webauthn]      origin (varint len + bytes), cross_origin u8,
	                                  extra_json (varint len + bytes), auth_suffix (varint len + bytes)

	Basic / Vesting proof = SignatureProof
	HTLC proof            = variant u8: 0 RegularTransfer { hash_depth u8, hash_root AnyHash,
	                                                        pre_image PreImage, SignatureProof }
	                                    1 EarlyResolve    { SignatureProof recipient, SignatureProof sender }
	                                    2 TimeoutResolve  { SignatureProof sender }
	        AnyHash  = algorithm u8 (1 blake2b, 3 sha256, 4 sha512) + 32 | 64 bytes
	        PreImage = length u8 (32 | 64) + bytes

Parsing is fail-closed: anything unexpected ends the walk and only the signers
decoded so far are returned. A signer that cannot be decoded simply never
matches a buyer.
*/

// Nimiq account types as reported in the RPC's fromType / toType fields.
const (
	AccountTypeBasic   = 0
	AccountTypeVesting = 1
	AccountTypeHTLC    = 2
	AccountTypeStaking = 3
)

// ProofSigners returns the normalised addresses derived from every public key
// in a transaction proof, given the sender account type. nil when the proof
// is empty, of an unsupported shape, or the sender type carries no signer
// (staking contract).
func ProofSigners(fromType int, proof []byte) []string {
	r := proofReader{b: proof}
	var out []string
	add := func() bool {
		addr, ok := r.signatureProof()
		if !ok {
			return false
		}
		if addr != "" {
			out = append(out, addr)
		}
		return true
	}
	switch fromType {
	case AccountTypeBasic, AccountTypeVesting:
		add()
	case AccountTypeHTLC:
		variant, ok := r.u8()
		if !ok {
			return nil
		}
		switch variant {
		case 0: // RegularTransfer
			if _, ok := r.u8(); !ok { // hash_depth
				return nil
			}
			if !r.anyHash() || !r.preImage() {
				return nil
			}
			add()
		case 1: // EarlyResolve: recipient then sender
			if add() {
				add()
			}
		case 2: // TimeoutResolve
			add()
		}
	}
	return dedupe(out)
}

type proofReader struct {
	b   []byte
	pos int
}

func (r *proofReader) u8() (byte, bool) {
	if r.pos >= len(r.b) {
		return 0, false
	}
	v := r.b[r.pos]
	r.pos++
	return v, true
}

func (r *proofReader) take(n int) ([]byte, bool) {
	if n < 0 || r.pos+n > len(r.b) {
		return nil, false
	}
	v := r.b[r.pos : r.pos+n]
	r.pos += n
	return v, true
}

// varint reads a postcard LEB128 unsigned length prefix.
func (r *proofReader) varint() (int, bool) {
	var v uint64
	for shift := uint(0); shift < 64; shift += 7 {
		c, ok := r.u8()
		if !ok {
			return 0, false
		}
		v |= uint64(c&0x7f) << shift
		if c&0x80 == 0 {
			if v > uint64(len(r.b)) {
				return 0, false
			}
			return int(v), true
		}
	}
	return 0, false
}

func (r *proofReader) lengthPrefixed() bool {
	n, ok := r.varint()
	if !ok {
		return false
	}
	_, ok = r.take(n)
	return ok
}

func (r *proofReader) anyHash() bool {
	alg, ok := r.u8()
	if !ok {
		return false
	}
	size := 32
	if alg == 4 {
		size = 64
	}
	_, ok = r.take(size)
	return ok
}

func (r *proofReader) preImage() bool {
	n, ok := r.u8()
	if !ok || (n != 32 && n != 64) {
		return false
	}
	_, ok = r.take(int(n))
	return ok
}

// signatureProof consumes one SignatureProof and returns the signer address.
// ok is false when the bytes do not form a proof; addr is empty for a key the
// address cannot be derived from (a multi-signature merkle path, whose
// account address is the merkle root rather than this key).
func (r *proofReader) signatureProof() (addr string, ok bool) {
	tf, ok := r.u8()
	if !ok {
		return "", false
	}
	algorithm, flags := tf&0x0f, tf>>4
	keyLen := 32
	switch algorithm {
	case 0:
	case 1:
		keyLen = 33
	default:
		return "", false
	}
	key, ok := r.take(keyLen)
	if !ok {
		return "", false
	}
	merkleNodes, ok := r.u8()
	if !ok {
		return "", false
	}
	if merkleNodes > 0 {
		if !r.lengthPrefixed() { // left_bits
			return "", false
		}
		count, ok := r.varint() // node hashes
		if !ok || count != int(merkleNodes) {
			return "", false
		}
		if _, ok := r.take(32 * count); !ok {
			return "", false
		}
	}
	if _, ok := r.take(64); !ok { // signature
		return "", false
	}
	if flags&0x1 != 0 { // webauthn extra fields
		if !r.lengthPrefixed() {
			return "", false
		}
		if _, ok := r.u8(); !ok {
			return "", false
		}
		for i := 0; i < 2; i++ { // client data JSON, then extension data
			if !r.lengthPrefixed() {
				return "", false
			}
		}
	}
	if merkleNodes > 0 {
		return "", true
	}
	return NormalizeAddress(addressFromKeyBytes(key)), true
}

// addressFromKeyBytes derives the address of an Ed25519 (32-byte) or ES256
// compressed (33-byte) public key: blake2b-256(key)[0:20], base32 + check.
func addressFromKeyBytes(key []byte) string {
	hash := HashPublicKey(key)
	base32Str := base32Enc.EncodeToString(hash)
	return "NQ" + iso7064Check("NQ00"+base32Str) + base32Str
}

func dedupe(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := in[:0]
	for _, s := range in {
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
