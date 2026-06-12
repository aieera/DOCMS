// CollaborationService gRPC adapter (read side).
//
// The graphql-gateway resolves the DocumentDetail `comments` and
// `annotations` fields via sedoc.v1.CollaborationService, but no
// service ever registered that server: the gateway's default upstream
// (collaboration:9090) points at the Node Yjs process, which speaks
// only WebSocket on :8083. Comments and annotations live in THIS
// service (comment_handler.go / annotation REST + service layer), so
// the gRPC surface belongs here too, on the same server and
// interceptor chain (tenant + user identity from metadata) as
// DocumentService.
//
// Only the two read RPCs the gateway calls are implemented; the
// embedded Unimplemented stub answers codes.Unimplemented for the
// rest (mutations arrive over REST today).
package handler

import (
	"context"
	"encoding/json"

	"google.golang.org/protobuf/types/known/timestamppb"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/repository"
	"github.com/aieera/sedoc/services/document/internal/service"
)

type CollabGRPC struct {
	sedocv1.UnimplementedCollaborationServiceServer
	svc *service.DocumentService
}

func NewCollabGRPC(svc *service.DocumentService) *CollabGRPC {
	return &CollabGRPC{svc: svc}
}

func (h *CollabGRPC) ListComments(ctx context.Context, req *sedocv1.ListCommentsRequest) (*sedocv1.ListCommentsResponse, error) {
	docID, err := parseUUID("document_id", req.GetDocumentId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	rows, err := h.svc.ListComments(ctx, docID, req.GetIncludeResolved())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	out := make([]*sedocv1.Comment, 0, len(rows))
	for i := range rows {
		out = append(out, commentToProto(&rows[i]))
	}
	// No cursor pagination on the underlying store — the full thread is
	// returned and PageResponse stays empty (the gateway treats an empty
	// next_cursor as "no more pages").
	return &sedocv1.ListCommentsResponse{Comments: out}, nil
}

func (h *CollabGRPC) ListAnnotations(ctx context.Context, req *sedocv1.ListAnnotationsRequest) (*sedocv1.ListAnnotationsResponse, error) {
	docID, err := parseUUID("document_id", req.GetDocumentId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	// The gateway sends no version — annotations overlay the current
	// version. A document with no content yet has nothing to annotate.
	doc, _, err := h.svc.GetDocument(ctx, docID)
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	if doc.CurrentVersionID == nil {
		return &sedocv1.ListAnnotationsResponse{}, nil
	}
	rows, err := h.svc.ListAnnotations(ctx, docID, *doc.CurrentVersionID)
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	out := make([]*sedocv1.Annotation, 0, len(rows))
	for i := range rows {
		a := &rows[i]
		if req.GetKind() != sedocv1.AnnotationKind_ANNOTATION_KIND_UNSPECIFIED &&
			annotationKindToProto(a.Type) != req.GetKind() {
			continue
		}
		if req.GetPage() != 0 && int32(a.PageNumber) != req.GetPage() {
			continue
		}
		out = append(out, annotationToProto(a))
	}
	return &sedocv1.ListAnnotationsResponse{Annotations: out}, nil
}

func commentToProto(c *repository.Comment) *sedocv1.Comment {
	out := &sedocv1.Comment{
		Id:         c.ID.String(),
		TenantId:   c.TenantID.String(),
		DocumentId: c.DocumentID.String(),
		AuthorId:   c.AuthorID.String(),
		Body:       c.Body,
		Resolved:   c.IsResolved,
		CreatedAt:  timestamppb.New(c.CreatedAt),
	}
	if c.ParentCommentID != nil {
		out.ParentId = c.ParentCommentID.String()
	}
	if c.ResolvedAt != nil {
		out.ResolvedAt = timestamppb.New(*c.ResolvedAt)
	}
	if c.ResolvedBy != nil {
		out.ResolvedBy = c.ResolvedBy.String()
	}
	return out
}

func annotationToProto(a *model.Annotation) *sedocv1.Annotation {
	geom := ""
	if a.Data != nil {
		if b, err := json.Marshal(a.Data); err == nil {
			geom = string(b)
		}
	}
	return &sedocv1.Annotation{
		Id:           a.ID.String(),
		TenantId:     a.TenantID.String(),
		DocumentId:   a.DocumentID.String(),
		AuthorId:     a.CreatedBy.String(),
		Kind:         annotationKindToProto(a.Type),
		Page:         int32(a.PageNumber),
		GeometryJson: geom,
		CreatedAt:    timestamppb.New(a.CreatedAt),
		UpdatedAt:    timestamppb.New(a.UpdatedAt),
	}
}

// annotationKindToProto maps the annotations table's type column to the
// proto enum. The ADR 0067 category values (pdf_markup, image_shape,
// video_timestamp) have no enum representation — the per-primitive kind
// for those lives inside annotation_data, which ships verbatim in
// geometry_json — so they map to UNSPECIFIED.
func annotationKindToProto(t string) sedocv1.AnnotationKind {
	switch t {
	case model.AnnotationTypeHighlight:
		return sedocv1.AnnotationKind_ANNOTATION_KIND_HIGHLIGHT
	case model.AnnotationTypeNote:
		return sedocv1.AnnotationKind_ANNOTATION_KIND_NOTE
	case model.AnnotationTypeStamp:
		return sedocv1.AnnotationKind_ANNOTATION_KIND_STAMP
	case model.AnnotationTypeDrawing:
		return sedocv1.AnnotationKind_ANNOTATION_KIND_DRAWING
	default:
		return sedocv1.AnnotationKind_ANNOTATION_KIND_UNSPECIFIED
	}
}
