// DSSSidecarSigner is the gRPC client for the Java DSS sidecar
// (services/signature-signer) introduced by ADR 0025.
//
// Wave 9.2b: the sidecar now produces real PAdES revisions, so this
// is the live client — it maps the package-local Request/Response to
// the signerv1 gRPC contract and back. The connection is lazy
// (grpc.NewClient) and auto-reconnects, so a sidecar restart doesn't
// wedge the signature service.
package signer

import (
	"context"
	"fmt"
	"time"

	signerv1 "github.com/aieera/sedoc/proto/gen/go/signerv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// DefaultSidecarAddr is the in-cluster address of the JVM sidecar.
const DefaultSidecarAddr = "signature-signer:6060"

// DSSSidecarSigner is the gRPC client to the DSS sidecar.
type DSSSidecarSigner struct {
	addr    string
	conn    *grpc.ClientConn
	rpc     signerv1.SignerServiceClient
	dialErr error
}

// NewDSSSidecarSigner constructs a sidecar client pointing at addr
// (empty → DefaultSidecarAddr). The connection is lazy: grpc.NewClient
// returns immediately and the TCP/HTTP2 handshake happens on the first
// RPC, so construction never blocks on the sidecar being up.
func NewDSSSidecarSigner(addr string) *DSSSidecarSigner {
	if addr == "" {
		addr = DefaultSidecarAddr
	}
	d := &DSSSidecarSigner{addr: addr}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		d.dialErr = err
		return d
	}
	d.conn = conn
	d.rpc = signerv1.NewSignerServiceClient(conn)
	return d
}

// Close releases the connection. Safe to call once at shutdown.
func (d *DSSSidecarSigner) Close() error {
	if d.conn != nil {
		return d.conn.Close()
	}
	return nil
}

// Sign produces a real PAdES revision via the sidecar.
func (d *DSSSidecarSigner) Sign(ctx context.Context, req Request) (*Response, error) {
	if err := validateCommon(req); err != nil {
		return nil, err
	}
	if d.dialErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrSidecarUnreachable, d.dialErr)
	}
	level := req.Level
	if level == "" {
		level = LevelBLT // package default per Wave 9 DoD
	}
	resp, err := d.rpc.Sign(ctx, &signerv1.SignRequest{
		PdfBytes:          req.PDFBytes,
		SignerName:        req.SignerName,
		SignerEmail:       req.SignerEmail,
		Reason:            req.Reason,
		Location:          req.Location,
		ContactInfo:       req.ContactInfo,
		FieldName:         req.FieldName,
		Level:             toProtoLevel(level),
		Mode:              toProtoMode(req.Mode),
		KmsAlias:          req.KMSAlias,
		DetachedSignature: req.DetachedSignature,
		CertChain:         req.CertChain,
		TsaUrl:            req.TSAURL,
	})
	if err != nil {
		return nil, mapSidecarErr(err)
	}
	signedAt, _ := time.Parse(time.RFC3339, resp.GetSignedAt())
	return &Response{
		PDFBytes:         resp.GetPdfBytes(),
		Level:            fromProtoLevel(resp.GetLevel()),
		SignedAt:         signedAt,
		Fingerprint:      resp.GetFingerprint(),
		ValidationReport: resp.GetValidationReport(),
	}, nil
}

// Verify inspects a signed PDF via the sidecar's DSS validator.
func (d *DSSSidecarSigner) Verify(ctx context.Context, pdfBytes []byte) (*VerificationReport, error) {
	if len(pdfBytes) == 0 {
		return nil, fmt.Errorf("%w: pdf_bytes empty", ErrInvalidRequest)
	}
	if d.dialErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrSidecarUnreachable, d.dialErr)
	}
	resp, err := d.rpc.Verify(ctx, &signerv1.VerifyRequest{PdfBytes: pdfBytes})
	if err != nil {
		return nil, mapSidecarErr(err)
	}
	out := &VerificationReport{
		SignatureCount: int(resp.GetSignatureCount()),
		LTVEnabled:     resp.GetLtvEnabled(),
		TamperEvident:  resp.GetTamperEvident(),
	}
	for _, s := range resp.GetSignatures() {
		signedAt, _ := time.Parse(time.RFC3339, s.GetSignedAt())
		out.Signatures = append(out.Signatures, SignatureInfo{
			SignerName: s.GetSignerName(),
			SignedAt:   signedAt,
			Issuer:     s.GetIssuer(),
			Valid:      s.GetValid(),
			Level:      fromProtoLevel(s.GetLevel()),
			Reason:     s.GetReason(),
		})
	}
	return out, nil
}

// mapSidecarErr turns a gRPC status into the package's typed errors so
// callers can branch with errors.Is for the right HTTP status upstream.
func mapSidecarErr(err error) error {
	switch status.Code(err) {
	case codes.InvalidArgument:
		return fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	case codes.Unavailable, codes.DeadlineExceeded:
		return fmt.Errorf("%w: %v", ErrSidecarUnreachable, err)
	default:
		return fmt.Errorf("signer: sidecar sign/verify failed: %w", err)
	}
}

func toProtoLevel(l Level) signerv1.Level {
	switch l {
	case LevelBB:
		return signerv1.Level_LEVEL_BB
	case LevelBT:
		return signerv1.Level_LEVEL_BT
	case LevelBLT:
		return signerv1.Level_LEVEL_BLT
	case LevelBLTA:
		return signerv1.Level_LEVEL_BLTA
	default:
		return signerv1.Level_LEVEL_UNSPECIFIED
	}
}

func fromProtoLevel(l signerv1.Level) Level {
	switch l {
	case signerv1.Level_LEVEL_BB:
		return LevelBB
	case signerv1.Level_LEVEL_BT:
		return LevelBT
	case signerv1.Level_LEVEL_BLT:
		return LevelBLT
	case signerv1.Level_LEVEL_BLTA:
		return LevelBLTA
	default:
		return ""
	}
}

func toProtoMode(m Mode) signerv1.Mode {
	switch m {
	case ModeServerHSM:
		return signerv1.Mode_MODE_SERVER_HSM
	case ModeUserHeld:
		return signerv1.Mode_MODE_USER_HELD
	default:
		return signerv1.Mode_MODE_UNSPECIFIED
	}
}

var _ Signer = (*DSSSidecarSigner)(nil)
