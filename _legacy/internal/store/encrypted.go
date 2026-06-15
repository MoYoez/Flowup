package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

// Cipher encrypts/decrypts at-rest payloads. Encrypt output is opaque bytes;
// Decrypt reverses it (and authenticates).
type Cipher interface {
	Encrypt(plaintext []byte) ([]byte, error)
	Decrypt(ciphertext []byte) ([]byte, error)
}

// AESGCM is AES-256-GCM with a random nonce prepended to each ciphertext.
type AESGCM struct{ aead cipher.AEAD }

// NewAESGCM builds a cipher from a 32-byte key.
func NewAESGCM(key []byte) (*AESGCM, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("aesgcm: key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &AESGCM{aead: aead}, nil
}

func (a *AESGCM) Encrypt(pt []byte) ([]byte, error) {
	nonce := make([]byte, a.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return a.aead.Seal(nonce, nonce, pt, nil), nil // nonce || ciphertext
}

func (a *AESGCM) Decrypt(ct []byte) ([]byte, error) {
	ns := a.aead.NonceSize()
	if len(ct) < ns {
		return nil, errors.New("aesgcm: ciphertext too short")
	}
	return a.aead.Open(nil, ct[:ns], ct[ns:], nil)
}

// Encrypted wraps a Store and transparently encrypts the payload fields
// (run input/output, step output, event data, side-effect result, human input,
// task payload/result) at rest. It is opt-in: wrap your store with it.
//
//	st, _ := store.NewSQLite(ctx, path)
//	ciph, _ := store.NewAESGCM(key)
//	st = store.NewEncrypted(st, ciph)
type Encrypted struct {
	inner  Store
	cipher Cipher
}

// NewEncrypted wraps inner so payload fields are encrypted at rest.
func NewEncrypted(inner Store, c Cipher) *Encrypted { return &Encrypted{inner: inner, cipher: c} }

func (e *Encrypted) enc(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return b, nil
	}
	ct, err := e.cipher.Encrypt(b)
	if err != nil {
		return nil, err
	}
	out := make([]byte, base64.StdEncoding.EncodedLen(len(ct)))
	base64.StdEncoding.Encode(out, ct)
	return out, nil
}

func (e *Encrypted) dec(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return b, nil
	}
	buf := make([]byte, base64.StdEncoding.DecodedLen(len(b)))
	n, err := base64.StdEncoding.Decode(buf, b)
	if err != nil {
		return nil, err
	}
	return e.cipher.Decrypt(buf[:n])
}

func (e *Encrypted) Init(ctx context.Context) error { return e.inner.Init(ctx) }
func (e *Encrypted) Close() error                   { return e.inner.Close() }

func (e *Encrypted) CreateRun(ctx context.Context, r RunRecord) (bool, RunRecord, error) {
	var err error
	if r.Input, err = e.enc(r.Input); err != nil {
		return false, RunRecord{}, err
	}
	if r.Output, err = e.enc(r.Output); err != nil {
		return false, RunRecord{}, err
	}
	created, existing, err := e.inner.CreateRun(ctx, r)
	if err != nil {
		return false, RunRecord{}, err
	}
	if !created {
		if err := e.decRun(&existing); err != nil {
			return false, RunRecord{}, err
		}
	}
	return created, existing, nil
}

func (e *Encrypted) GetRun(ctx context.Context, id string) (RunRecord, bool, error) {
	return e.getRun(e.inner.GetRun(ctx, id))
}
func (e *Encrypted) GetRunByResumeToken(ctx context.Context, t string) (RunRecord, bool, error) {
	return e.getRun(e.inner.GetRunByResumeToken(ctx, t))
}
func (e *Encrypted) GetRunByIdempotencyKey(ctx context.Context, k string) (RunRecord, bool, error) {
	return e.getRun(e.inner.GetRunByIdempotencyKey(ctx, k))
}
func (e *Encrypted) getRun(r RunRecord, ok bool, err error) (RunRecord, bool, error) {
	if err != nil || !ok {
		return r, ok, err
	}
	if derr := e.decRun(&r); derr != nil {
		return RunRecord{}, false, derr
	}
	return r, true, nil
}
func (e *Encrypted) decRun(r *RunRecord) error {
	var err error
	if r.Input, err = e.dec(r.Input); err != nil {
		return err
	}
	r.Output, err = e.dec(r.Output)
	return err
}

func (e *Encrypted) UpdateRun(ctx context.Context, r RunRecord) error {
	var err error
	if r.Input, err = e.enc(r.Input); err != nil {
		return err
	}
	if r.Output, err = e.enc(r.Output); err != nil {
		return err
	}
	return e.inner.UpdateRun(ctx, r)
}

func (e *Encrypted) ListPendingRuns(ctx context.Context) ([]RunRecord, error) {
	rs, err := e.inner.ListPendingRuns(ctx)
	if err != nil {
		return nil, err
	}
	for i := range rs {
		if err := e.decRun(&rs[i]); err != nil {
			return nil, err
		}
	}
	return rs, nil
}

func (e *Encrypted) GetStep(ctx context.Context, runID, key string) (StepRecord, bool, error) {
	rec, ok, err := e.inner.GetStep(ctx, runID, key)
	if err != nil || !ok {
		return rec, ok, err
	}
	if rec.Output, err = e.dec(rec.Output); err != nil {
		return StepRecord{}, false, err
	}
	return rec, true, nil
}

func (e *Encrypted) PutStep(ctx context.Context, r StepRecord) error {
	var err error
	if r.Output, err = e.enc(r.Output); err != nil {
		return err
	}
	return e.inner.PutStep(ctx, r)
}

func (e *Encrypted) AppendEvent(ctx context.Context, ev EventRecord) error {
	var err error
	if ev.Data, err = e.enc(ev.Data); err != nil {
		return err
	}
	return e.inner.AppendEvent(ctx, ev)
}

func (e *Encrypted) ListEvents(ctx context.Context, runID string) ([]EventRecord, error) {
	evs, err := e.inner.ListEvents(ctx, runID)
	if err != nil {
		return nil, err
	}
	for i := range evs {
		if evs[i].Data, err = e.dec(evs[i].Data); err != nil {
			return nil, err
		}
	}
	return evs, nil
}

func (e *Encrypted) ClaimSideEffect(ctx context.Context, key string) (bool, []byte, error) {
	claimed, existing, err := e.inner.ClaimSideEffect(ctx, key)
	if err != nil {
		return false, nil, err
	}
	if !claimed {
		if existing, err = e.dec(existing); err != nil {
			return false, nil, err
		}
	}
	return claimed, existing, nil
}

func (e *Encrypted) FinishSideEffect(ctx context.Context, key string, result []byte) error {
	var err error
	if result, err = e.enc(result); err != nil {
		return err
	}
	return e.inner.FinishSideEffect(ctx, key, result)
}

func (e *Encrypted) PutHumanInput(ctx context.Context, runID, nodeID string, data []byte) error {
	var err error
	if data, err = e.enc(data); err != nil {
		return err
	}
	return e.inner.PutHumanInput(ctx, runID, nodeID, data)
}

func (e *Encrypted) GetHumanInput(ctx context.Context, runID, nodeID string) ([]byte, bool, error) {
	data, ok, err := e.inner.GetHumanInput(ctx, runID, nodeID)
	if err != nil || !ok {
		return data, ok, err
	}
	if data, err = e.dec(data); err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func (e *Encrypted) PurgeRuns(ctx context.Context, before string) (int, error) {
	return e.inner.PurgeRuns(ctx, before)
}

func (e *Encrypted) EnqueueOutbox(ctx context.Context, key string, payload []byte) error {
	enc, err := e.enc(payload)
	if err != nil {
		return err
	}
	return e.inner.EnqueueOutbox(ctx, key, enc)
}

func (e *Encrypted) ClaimPendingOutbox(ctx context.Context, limit int) ([]OutboxEntry, error) {
	entries, err := e.inner.ClaimPendingOutbox(ctx, limit)
	if err != nil {
		return nil, err
	}
	for i := range entries {
		if entries[i].Payload, err = e.dec(entries[i].Payload); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

func (e *Encrypted) MarkDelivered(ctx context.Context, key string) error {
	return e.inner.MarkDelivered(ctx, key)
}

// Task queue: encrypt payload + result.

func (e *Encrypted) EnqueueTask(ctx context.Context, t TaskRecord) error {
	var err error
	if t.Payload, err = e.enc(t.Payload); err != nil {
		return err
	}
	if t.Result, err = e.enc(t.Result); err != nil {
		return err
	}
	return e.inner.EnqueueTask(ctx, t)
}

func (e *Encrypted) ClaimTask(ctx context.Context, workerID string, caps []string, leaseMS int64) (TaskRecord, bool, error) {
	rec, ok, err := e.inner.ClaimTask(ctx, workerID, caps, leaseMS)
	if err != nil || !ok {
		return rec, ok, err
	}
	if err := e.decTask(&rec); err != nil {
		return TaskRecord{}, false, err
	}
	return rec, true, nil
}

func (e *Encrypted) GetTask(ctx context.Context, taskID string) (TaskRecord, bool, error) {
	rec, ok, err := e.inner.GetTask(ctx, taskID)
	if err != nil || !ok {
		return rec, ok, err
	}
	if err := e.decTask(&rec); err != nil {
		return TaskRecord{}, false, err
	}
	return rec, true, nil
}

func (e *Encrypted) decTask(t *TaskRecord) error {
	var err error
	if t.Payload, err = e.dec(t.Payload); err != nil {
		return err
	}
	t.Result, err = e.dec(t.Result)
	return err
}

func (e *Encrypted) CompleteTask(ctx context.Context, taskID, workerID, status string, result []byte) (bool, error) {
	var err error
	if result, err = e.enc(result); err != nil {
		return false, err
	}
	return e.inner.CompleteTask(ctx, taskID, workerID, status, result)
}

func (e *Encrypted) HeartbeatTask(ctx context.Context, taskID, workerID string, leaseMS int64) (bool, error) {
	return e.inner.HeartbeatTask(ctx, taskID, workerID, leaseMS)
}

func (e *Encrypted) ReclaimExpiredTasks(ctx context.Context) (int, error) {
	return e.inner.ReclaimExpiredTasks(ctx)
}
