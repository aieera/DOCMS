// Package signer is the Go client for the JVM DSS PAdES sidecar
// (services/signature-signer). The signature service computes/obtains the
// signing material and the sidecar produces the actual PAdES revision; this
// wraps that gRPC contract (proto/signer.proto → signerv1).
//
// Two modes mirror the proto:
//   - Seal (MODE_SERVER_HSM): the sidecar signs with its own server key — the
//     organizational-seal / workflow-sealing path.
//   - Embed (MODE_USER_HELD): the caller supplies a detached CMS signature +
//     chain (QES) and the sidecar only embeds it — the qes.go hand-off.
package signer

import (
	"context"
	"fmt"

	signerv1 "github.com/aieera/sedoc/proto/gen/go/signerv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Client is a thin wrapper over the signer sidecar gRPC. Safe for concurrent
// use; one per process. Close on shutdown.
type Client struct {
	conn *grpc.ClientConn
	rpc  signerv1.SignerServiceClient
}

// DefaultAddr is the in-cluster address of the sidecar.
const DefaultAddr = "signature-signer:6060"

// Dial connects to the sidecar. addr empty → DefaultAddr. Uses a lazy,
// auto-reconnecting connection (grpc.NewClient) so a sidecar restart doesn't
// wedge the caller.
func Dial(addr string) (*Client, error) {
	if addr == "" {
		addr = DefaultAddr
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("signer dial %s: %w", addr, err)
	}
	return &Client{conn: conn, rpc: signerv1.NewSignerServiceClient(conn)}, nil
}

// Close releases the underlying connection.
func (c *Client) Close() error { return c.conn.Close() }

// SealRequest configures a server-key seal.
type SealRequest struct {
	PDF        []byte
	SignerName string
	Reason     string
	Location   string
	Level      signerv1.Level // 0 → sidecar default (B-B offline, higher with a TSA)
	TSAURL     string         // empty → sidecar's SIGNER_TSA_URL default
}

// SealResult is the produced revision.
type SealResult struct {
	PDF         []byte
	Level       signerv1.Level
	SignedAt    string
	Fingerprint string
}

// Seal signs pdf with the sidecar's server key (MODE_SERVER_HSM) — the
// organizational-seal / workflow-completion path.
func (c *Client) Seal(ctx context.Context, in SealRequest) (*SealResult, error) {
	resp, err := c.rpc.Sign(ctx, &signerv1.SignRequest{
		PdfBytes:   in.PDF,
		SignerName: in.SignerName,
		Reason:     in.Reason,
		Location:   in.Location,
		Level:      in.Level,
		Mode:       signerv1.Mode_MODE_SERVER_HSM,
		TsaUrl:     in.TSAURL,
	})
	if err != nil {
		return nil, err
	}
	return &SealResult{
		PDF:         resp.GetPdfBytes(),
		Level:       resp.GetLevel(),
		SignedAt:    resp.GetSignedAt(),
		Fingerprint: resp.GetFingerprint(),
	}, nil
}

// Embed embeds a caller-supplied detached CMS signature + cert chain into pdf
// (MODE_USER_HELD) — the QES hand-off (qes.go). The sidecar must implement the
// user-held branch for this to succeed.
func (c *Client) Embed(ctx context.Context, pdf, detachedCMS []byte, chain [][]byte, level signerv1.Level) (*SealResult, error) {
	resp, err := c.rpc.Sign(ctx, &signerv1.SignRequest{
		PdfBytes:          pdf,
		DetachedSignature: detachedCMS,
		CertChain:         chain,
		Level:             level,
		Mode:              signerv1.Mode_MODE_USER_HELD,
	})
	if err != nil {
		return nil, err
	}
	return &SealResult{PDF: resp.GetPdfBytes(), Level: resp.GetLevel(), SignedAt: resp.GetSignedAt(), Fingerprint: resp.GetFingerprint()}, nil
}

// Verify inspects a signed pdf via the sidecar's DSS validator.
func (c *Client) Verify(ctx context.Context, pdf []byte) (*signerv1.VerifyResponse, error) {
	return c.rpc.Verify(ctx, &signerv1.VerifyRequest{PdfBytes: pdf})
}
