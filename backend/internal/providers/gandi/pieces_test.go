package gandi

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestExtrairePiece(t *testing.T) {
	excel := []byte("PK\x03\x04 faux excel")
	raw := strings.ReplaceAll(`From: Satish <satish@stellar.example>
To: mathias.coutant@pxcom.aero
Subject: Etihad 1226 - Stellar
Message-ID: <abc@stellar.example>
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="X"

--X
Content-Type: text/plain; charset=utf-8

Please find the metadata for the December cycle.
--X
Content-Type: application/vnd.openxmlformats-officedocument.spreadsheetml.sheet; name="EY_1226_PXCom-V1.xlsx"
Content-Disposition: attachment; filename="EY_1226_PXCom-V1.xlsx"
Content-Transfer-Encoding: base64

`+base64.StdEncoding.EncodeToString(excel)+`
--X--
`, "\n", "\r\n")
	got, err := extrairePiece([]byte(raw), "EY_1226_PXCom-V1.xlsx")
	if err != nil || !bytes.Equal(got, excel) {
		t.Fatalf("pièce = %q, %v", got, err)
	}
	if _, err := extrairePiece([]byte(raw), "autre.xlsx"); err != ErrPieceIntrouvable {
		t.Fatalf("fichier absent : %v", err)
	}
}
