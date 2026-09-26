package app

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Iyed-M/offbeat/internal/acquisition"
)

const selectionReceiptLifetime = 10 * time.Minute

// Receipts are self-contained, bounded evidence of one daemon inspection. The
// process-local key deliberately invalidates outstanding inspections on restart.
type selectionEvidence struct {
	Track    acquisition.InspectionTrack  `json:"track"`
	VideoID  string                       `json:"video_id"`
	Reason   acquisition.ResolutionReason `json:"reason"`
	Revision int64                        `json:"revision"`
	Issued   int64                        `json:"issued"`
}

func (d *Daemon) receiptKey() error {
	d.selectionKeyOnce.Do(func() { _, d.selectionKeyErr = rand.Read(d.selectionKey[:]) })
	return d.selectionKeyErr
}

func (d *Daemon) signSelection(e selectionEvidence) (string, error) {
	if err := d.receiptKey(); err != nil {
		return "", err
	}
	data, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, d.selectionKey[:])
	mac.Write(data)
	return base64.RawURLEncoding.EncodeToString(data) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (d *Daemon) verifySelection(token string) (selectionEvidence, error) {
	var e selectionEvidence
	if len(token) == 0 || len(token) > 8192 {
		return e, errors.New("invalid inspection receipt")
	}
	if err := d.receiptKey(); err != nil {
		return e, err
	}
	left, right, ok := strings.Cut(token, ".")
	if !ok {
		return e, errors.New("invalid inspection receipt")
	}
	data, err := base64.RawURLEncoding.DecodeString(left)
	if err != nil {
		return e, errors.New("invalid inspection receipt")
	}
	sig, err := base64.RawURLEncoding.DecodeString(right)
	if err != nil {
		return e, errors.New("invalid inspection receipt")
	}
	mac := hmac.New(sha256.New, d.selectionKey[:])
	mac.Write(data)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return e, errors.New("invalid inspection receipt")
	}
	if err := json.Unmarshal(data, &e); err != nil {
		return e, errors.New("invalid inspection receipt")
	}
	now := time.Now().Unix()
	if e.Issued > now || now-e.Issued > int64(selectionReceiptLifetime.Seconds()) {
		return e, errors.New("inspection expired; refresh candidates")
	}
	return e, nil
}
