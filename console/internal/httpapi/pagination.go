package httpapi

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sqlrush/airush/console/internal/repo"
	"github.com/sqlrush/airush/libs/apierror"
)

const (
	defaultLimit = 50
	maxLimit     = 200
)

// encodeCursor 生成不透明游标：base64url("unixnano.id")——调用方禁依赖内部结构。
func encodeCursor(createdAt time.Time, id string) string {
	raw := fmt.Sprintf("%d.%s", createdAt.UnixNano(), id)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// parsePageParams 解析 ?cursor=&limit=；篡改/非法游标 → 400（spec-1.1 T8）。
func parsePageParams(r *http.Request) (*repo.PageCursor, int, error) {
	limit := defaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxLimit {
			return nil, 0, apierror.New(apierror.CodeValidationFailed).WithDetails(
				apierror.Detail{Field: "limit", Reason: fmt.Sprintf("必须是 1..%d 的整数", maxLimit)})
		}
		limit = n
	}

	rawCursor := r.URL.Query().Get("cursor")
	if rawCursor == "" {
		return nil, limit, nil
	}
	cur, err := decodeCursor(rawCursor)
	if err != nil {
		return nil, 0, apierror.New(apierror.CodeValidationFailed).WithDetails(
			apierror.Detail{Field: "cursor", Reason: "游标无效"})
	}
	return cur, limit, nil
}

func decodeCursor(raw string) (*repo.PageCursor, error) {
	buf, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("decode cursor: %w", err)
	}
	nano, id, ok := strings.Cut(string(buf), ".")
	if !ok || !isUUID(id) {
		return nil, fmt.Errorf("cursor structure invalid")
	}
	n, err := strconv.ParseInt(nano, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("cursor timestamp invalid: %w", err)
	}
	return &repo.PageCursor{CreatedAt: time.Unix(0, n).UTC(), ID: id}, nil
}
