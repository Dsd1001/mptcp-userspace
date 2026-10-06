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

func TestMPX4StableOfficialKeyScheduleVector(t *testing.T) {
	key := mustHex(t, "a0a1a2a3a4a5a6a7a8a9aaabacadaeafb0b1b2b3b4b5b6b7b8b9babbbcbdbebf")
	preface := mustHex(t, "4d50580004")
	ci := mustHex(t, "01405a01001000112233445566778899aabbccddeeff020001000300010104000100050020000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f070004800080000800048001000009000248000a01024060")
	si := mustHex(t, "023b060020202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f070004800080000800048001000009000248000a01024080")
	h0 := sha256.Sum256(bytesJoin(preface, ci, si))
	requireHex(t, "H0", h0[:], "53731db4646e0c60563a772a9d1a1d59ad35a3071d13f0112fbe5b63a444e2d4")

	early := hkdfExtract(make([]byte, 32), key)
	requireHex(t, "early_secret", early, "fff6bab75d4df43e3a58d45a6b3d6ebaa0f75da67d71520fc66467d24f537e5e")
	hs, err := handshakeSecretFor(key, h0[:])
	if err != nil {
		t.Fatal(err)
	}
	requireHex(t, "handshake_secret", hs, "8c1032bba8ab3e3f74a4b395db4238dae2ffe59f5b3be5674062aa07a6fab462")

	cfk, _ := finishedKey(hs, true)
	requireHex(t, "client_finished_key", cfk, "44b1c63e5e9901009cba066d69e731253845f2530a1c165c084dd560f08242ac")
	sfk, _ := finishedKey(hs, false)
	requireHex(t, "server_finished_key", sfk, "a3f2ce077f1505c2dde142cf19e33ac4ec14e001b0814d28e8f0fac15c20f74f")

	cv := finishedVerify(cfk, h0[:])
	requireHex(t, "client_verify", cv, "ac03617663c756fbc623940cccaa9cd144c7a0dfaa3f7c0dea29f9a56b8eb189")
	cf, _ := encodeHandshakeMessage(mpx4HSClientFinished, cv)
	requireHex(t, "client_finished", cf, "0320ac03617663c756fbc623940cccaa9cd144c7a0dfaa3f7c0dea29f9a56b8eb189")
	h1 := sha256.Sum256(bytesJoin(preface, ci, si, cf))
	requireHex(t, "H1", h1[:], "00466799c19b26106f7b6e9a2b37f5b7ae064d4d8df16a9cb667bfc7e9422586")

	sv := finishedVerify(sfk, h1[:])
	requireHex(t, "server_verify", sv, "c607d316e0e32d5cf7ec48583b31e3d0e511ff9b701b317f989fb320fa1ae331")
	sf, _ := encodeHandshakeMessage(mpx4HSServerFinished, sv)
	requireHex(t, "server_finished", sf, "0420c607d316e0e32d5cf7ec48583b31e3d0e511ff9b701b317f989fb320fa1ae331")
	h2 := sha256.Sum256(bytesJoin(preface, ci, si, cf, sf))
	requireHex(t, "H2", h2[:], "c1e8cd1d94ec2902317d8d65efd5542098500a669f88f032727e24e539181399")

	cs, _ := mpx4ExpandLabel(hs, "client application", h2[:], 32)
	requireHex(t, "client_app", cs, "72407efc78d513205dd6fa564b0d4fa0ebf022637fa879fd1f139130be896876")
	ss, _ := mpx4ExpandLabel(hs, "server application", h2[:], 32)
	requireHex(t, "server_app", ss, "e40937d892d0b5218d5e1c93e89277672cbed12ae21cf25532259e1513a5122c")
	ck, _ := mpx4ExpandLabel(cs, "key", nil, 32)
	requireHex(t, "client_key", ck, "095e2f7dc0505642121edf7613944f62d1e5d185bf431fcebcd7862a92f21eac")
	civ, _ := mpx4ExpandLabel(cs, "iv", nil, 12)
	requireHex(t, "client_iv", civ, "125d55dacaf4bb44dc69329f")
	sk, _ := mpx4ExpandLabel(ss, "key", nil, 32)
	requireHex(t, "server_key", sk, "cd9237ecd5f142914846cf519ebafa8c3e9c533b5c9b218b74e94df5a45f4055")
	siv, _ := mpx4ExpandLabel(ss, "iv", nil, 12)
	requireHex(t, "server_iv", siv, "c8f2f857f3accef8b0993213")
}

func TestMPX4StableOfficialFrameVectors(t *testing.T) {
	cases := []struct {
		f    frame
		wire string
	}{
		{frame{kind: kindData, stream: 1, offset: 32768, id: 7, data: []byte("hello")}, "130b01800080000768656c6c6f"},
		{frame{kind: kindACK, stream: 1, id: 7, offset: 1234567}, "140601078012d687"},
		{frame{kind: kindWindow, stream: 1, offset: 65536, id: 131072}, "1509018001000080020000"},
		{frame{kind: kindSessionWindow, offset: 1048576, id: 8388608}, "20088010000080800000"},
		{frame{kind: kindCreditProbe, stream: 0}, "210100"},
		{frame{kind: kindTransmissionRetire, offset: 7}, "1a0107"},
	}
	for _, tc := range cases {
		got, err := encodeV4Frame(tc.f)
		if err != nil {
			t.Fatal(err)
		}
		requireHex(t, "frame", got, tc.wire)
	}
}

func TestMPX4StableOfficialSecureRecordVectors(t *testing.T) {
	key := mustHex(t, "095e2f7dc0505642121edf7613944f62d1e5d185bf431fcebcd7862a92f21eac")
	a, err := aeadFor(key)
	if err != nil {
		t.Fatal(err)
	}
	var iv [12]byte
	copy(iv[:], mustHex(t, "125d55dacaf4bb44dc69329f"))
	cases := []struct {
		seq                 uint64
		plain, header, wire string
	}{
		{0, "130b01800080000768656c6c6f", "000d", "000d406d7fae1794dd7ff5848ed2ed85807a6e5c368cbe170c51118788869c"},
		{1, "010101", "0003", "00037754b00910d0193ae9a8cd76a7a8f6cd0eebc8"},
	}
	for _, tc := range cases {
		h := mustHex(t, tc.header)
		sealed := a.Seal(nil, xorV4Nonce(iv, tc.seq), mustHex(t, tc.plain), h)
		got := append(append([]byte(nil), h...), sealed...)
		requireHex(t, "secure record", got, tc.wire)
	}
}
