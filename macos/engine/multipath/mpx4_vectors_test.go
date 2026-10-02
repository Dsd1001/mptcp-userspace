package multipath

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func requireHex(t *testing.T, name string, got []byte, want string) {
	t.Helper()
	w := mustHex(t, want)
	if !bytes.Equal(got, w) {
		t.Fatalf("%s\ngot  %x\nwant %x", name, got, w)
	}
}

func TestMPX4Draft03OfficialKeyScheduleVector(t *testing.T) {
	key := mustHex(t, "a0a1a2a3a4a5a6a7a8a9aaabacadaeafb0b1b2b3b4b5b6b7b8b9babbbcbdbebf")
	preface := mustHex(t, "4d50580004")
	ci := mustHex(t, "01405901001000112233445566778899aabbccddeeff020001000300010104000100050020000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f0700048000800008000480010000090002480010000101")
	si := mustHex(t, "023a060020202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f0700048000800008000480010000090002480010000101")
	h0 := sha256.Sum256(bytesJoin(preface, ci, si))
	requireHex(t, "H0", h0[:], "fd16064627efc15ce25927f517751056abdfb0cd047fdbbb17e1e5ef18c0998f")
	early := hkdfExtract(make([]byte, 32), key)
	requireHex(t, "early_secret", early, "fff6bab75d4df43e3a58d45a6b3d6ebaa0f75da67d71520fc66467d24f537e5e")
	hs, err := handshakeSecretFor(key, h0[:])
	if err != nil {
		t.Fatal(err)
	}
	requireHex(t, "handshake_secret", hs, "8a5547746bd92026081fcf47b40091baeb25c0d38a88af1b99a16ceec0681875")
	cfk, _ := finishedKey(hs, true)
	requireHex(t, "client_finished_key", cfk, "0d38dc2579d92953901a28c793a2723e3eed077012c407a3e32317ad44d09510")
	sfk, _ := finishedKey(hs, false)
	requireHex(t, "server_finished_key", sfk, "e7255729181a1464572035b3739f0c485849214aa2aea62aaadd3e972a3d2a44")
	cv := finishedVerify(cfk, h0[:])
	requireHex(t, "client_verify", cv, "4344ac42b1062577ca0f40753ed527f5ca7c90211962d18f4e9128c1301ce902")
	cf, _ := encodeHandshakeMessage(mpx4HSClientFinished, cv)
	requireHex(t, "client_finished", cf, "03204344ac42b1062577ca0f40753ed527f5ca7c90211962d18f4e9128c1301ce902")
	h1 := sha256.Sum256(bytesJoin(preface, ci, si, cf))
	requireHex(t, "H1", h1[:], "3227620c21a9d43be148759bbc0aa8b97f4b88efe3f53ae775e8e953646b9f95")
	sv := finishedVerify(sfk, h1[:])
	requireHex(t, "server_verify", sv, "f9f52ecc65a11deececfabf5263f255197e7e674239831c0593d26b3d07649ac")
	sf, _ := encodeHandshakeMessage(mpx4HSServerFinished, sv)
	requireHex(t, "server_finished", sf, "0420f9f52ecc65a11deececfabf5263f255197e7e674239831c0593d26b3d07649ac")
	h2 := sha256.Sum256(bytesJoin(preface, ci, si, cf, sf))
	requireHex(t, "H2", h2[:], "17db858018c52e2cd77c46e377e0bd6359a92a5b9f37a9165865c3bbfbd56752")
	cs, _ := mpx4ExpandLabel(hs, "client application", h2[:], 32)
	requireHex(t, "client_app", cs, "fd3e1eea8084f00d5639a048ae3e122293f7149af76690d503d1f20b83f9d3e0")
	ss, _ := mpx4ExpandLabel(hs, "server application", h2[:], 32)
	requireHex(t, "server_app", ss, "51636e320a7176e721f46673fcd68ed9cef185c8c417880ebc3203be98e308f2")
	ck, _ := mpx4ExpandLabel(cs, "key", nil, 32)
	requireHex(t, "client_key", ck, "77073510941704d465e98dbb9542bda302465094bef1a8f1a50312b730e7ef68")
	civ, _ := mpx4ExpandLabel(cs, "iv", nil, 12)
	requireHex(t, "client_iv", civ, "1f0cc87dcd985279eefbc191")
	sk, _ := mpx4ExpandLabel(ss, "key", nil, 32)
	requireHex(t, "server_key", sk, "df82c7578fa2c774c7730f3704cc429beab04a61bb6723c0034fe517a974566d")
	siv, _ := mpx4ExpandLabel(ss, "iv", nil, 12)
	requireHex(t, "server_iv", siv, "1a4f47ec14b6f5c3673e9c8a")
}

func TestMPX4Draft03OfficialFrameVectors(t *testing.T) {
	cases := []struct {
		f    frame
		wire string
	}{
		{frame{kind: kindData, stream: 1, offset: 32768, id: 7, data: []byte("hello")}, "130b01800080000768656c6c6f"},
		{frame{kind: kindACK, stream: 1, id: 7, offset: 1234567}, "140601078012d687"},
		{frame{kind: kindWindow, stream: 1, offset: 65536, id: 131072}, "1509018001000080020000"},
		{frame{kind: kindSessionWindow, offset: 1048576, id: 8388608}, "20088010000080800000"},
		{frame{kind: kindCreditProbe, stream: 0}, "210100"},
	}
	for _, tc := range cases {
		got, err := encodeV4Frame(tc.f)
		if err != nil {
			t.Fatal(err)
		}
		requireHex(t, "frame", got, tc.wire)
	}
}

func TestMPX4Draft03OfficialSecureRecordVectors(t *testing.T) {
	key := mustHex(t, "77073510941704d465e98dbb9542bda302465094bef1a8f1a50312b730e7ef68")
	a, err := aeadFor(key)
	if err != nil {
		t.Fatal(err)
	}
	var iv [12]byte
	copy(iv[:], mustHex(t, "1f0cc87dcd985279eefbc191"))
	cases := []struct {
		seq                 uint64
		plain, header, wire string
	}{
		{0, "130b01800080000768656c6c6f", "000d", "000dd48ca4d88424c813baa4aeffbc7867966596e11781365723a7bbb248e9"},
		{1, "010101", "0003", "000300b90cec9b2be63ff0594a681bfbea22502c19"},
	}
	for _, tc := range cases {
		h := mustHex(t, tc.header)
		sealed := a.Seal(nil, xorV4Nonce(iv, tc.seq), mustHex(t, tc.plain), h)
		got := append(append([]byte(nil), h...), sealed...)
		requireHex(t, "secure record", got, tc.wire)
	}
}
