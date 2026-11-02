package fund

// 手动充值截图与后台收款二维码共用上传机制。
// 安全口径（与资金服务其余部分一致）：
//   - 只接受 jpg/png：服务端以 DetectContentType 嗅探真实类型为准，不信任客户端传的 Content-Type 与文件名；
//   - 单张 <= 5MB：超限直接拒绝，不落盘；
//   - 文件名服务端随机生成（file_id + 按真实类型定的后缀），绝不含用户可控内容；
//   - 存储目录只来自配置 FUND_UPLOAD_DIR（默认 /opt/xianliao/fund-uploads），取用一律走鉴权接口
//     GET /v1/uploads/{file_id}（本人凭证或管理员可看；启用中收款方式的二维码可供已鉴权用户取用），
//     绝不做公开直链、不把磁盘路径暴露给客户端（返回的只是 /v1/uploads/<file_id> 相对路径）。
//   - 凭证重传时旧文件行与磁盘文件一并清理（替换语义，见 manual_recharge.go）。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxUploadBytes = 5 << 20 // 5MB
	uploadKindQR   = "qr"
	uploadKindProof = "proof"
)

var (
	errUploadTooLarge  = errors.New("uploaded file exceeds 5MB limit")
	errUploadBadType   = errors.New("uploaded file must be jpg or png")
	errUploadNotFound  = errors.New("upload file not found")
	errUploadNoFile    = errors.New("no file uploaded")
)

// uploadDir：配置目录；测试注入 Config.UploadDir，生产默认 /opt/xianliao/fund-uploads。
func (s *Server) uploadDir() string {
	if s.cfg.UploadDir != "" {
		return s.cfg.UploadDir
	}
	return "/opt/xianliao/fund-uploads"
}

// saveUpload：校验并落盘 + 写 fund_upload_file 行。调用方负责在业务事务外调用
// （文件落盘不可回滚；业务行写入失败时调用方须调 deleteUploadFile 清理孤文件，见各调用点）。
func (s *Server) saveUpload(ctx context.Context, kind string, fh *multipart.FileHeader) (fileID, storagePath, contentType string, size int64, err error) {
	f, err := fh.Open()
	if err != nil {
		return "", "", "", 0, err
	}
	defer f.Close()
	// 先读前 512 字节嗅探真实类型，再限流读剩余（5MB+1，超限即拒）。
	head := make([]byte, 512)
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return "", "", "", 0, err
	}
	head = head[:n]
	contentType = http.DetectContentType(head)
	var ext string
	switch contentType {
	case "image/jpeg":
		ext = ".jpg"
	case "image/png":
		ext = ".png"
	default:
		return "", "", "", 0, errUploadBadType
	}
	rest, err := io.ReadAll(io.LimitReader(f, maxUploadBytes+1-int64(n)))
	if err != nil {
		return "", "", "", 0, err
	}
	size = int64(n) + int64(len(rest))
	if size > maxUploadBytes {
		return "", "", "", 0, errUploadTooLarge
	}
	if size == 0 {
		return "", "", "", 0, errUploadBadType
	}
	fileID = newID("upl_")
	name := fileID + ext
	storagePath = filepath.Join(s.uploadDir(), name)
	if err := os.MkdirAll(s.uploadDir(), 0o755); err != nil {
		return "", "", "", 0, err
	}
	data := append(append([]byte{}, head...), rest...)
	if err := os.WriteFile(storagePath, data, 0o644); err != nil {
		return "", "", "", 0, err
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO fund_upload_file (file_id, kind, content_type, size_bytes, storage_path) VALUES ($1,$2,$3,$4,$5)`,
		fileID, kind, contentType, size, storagePath); err != nil {
		_ = os.Remove(storagePath)
		return "", "", "", 0, err
	}
	return fileID, storagePath, contentType, size, nil
}

// deleteUploadFile：删文件行 + 磁盘文件（尽力而为；磁盘删除失败只记日志，不阻断业务）。
func (s *Server) deleteUploadFile(ctx context.Context, fileID string) {
	var storagePath string
	err := s.db.QueryRowContext(ctx, `SELECT storage_path FROM fund_upload_file WHERE file_id=$1`, fileID).Scan(&storagePath)
	if err != nil {
		return
	}
	// 防御性路径校验：只删配置目录内的文件。
	clean := filepath.Clean(storagePath)
	if !strings.HasPrefix(clean, filepath.Clean(s.uploadDir())+string(os.PathSeparator)) {
		s.log.Printf("refuse to delete upload outside dir: %s", clean)
		return
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM fund_upload_file WHERE file_id=$1`, fileID); err != nil {
		s.log.Printf("delete upload row failed file=%s err=%v", fileID, err)
		return
	}
	if err := os.Remove(clean); err != nil && !os.IsNotExist(err) {
		s.log.Printf("remove upload file failed file=%s err=%v", fileID, err)
	}
}

// canViewUpload：取用鉴权。管理员（X-Admin-Secret）一律可看；用户侧：
//   - kind=qr：该文件被某个启用中收款方式引用时，任意已鉴权用户可看（用户选方式付款必须能扫码）；
//   - kind=proof：仅凭证上传人本人可看；
//   - 其余一律不可看（统一回 404，不透露文件存在性）。
func (s *Server) canViewUpload(ctx context.Context, uid string, fileID string) (string, string, bool) {
	var kind, contentType, storagePath string
	err := s.db.QueryRowContext(ctx,
		`SELECT kind, content_type, storage_path FROM fund_upload_file WHERE file_id=$1`, fileID).
		Scan(&kind, &contentType, &storagePath)
	if err == sql.ErrNoRows {
		return "", "", false
	}
	if err != nil {
		return "", "", false
	}
	switch kind {
	case uploadKindQR:
		// 启用中收款方式的二维码（付款时扫码）或本人正常提现方式的收款二维码（人工打款时查看）均可看。
		var m string
		err := s.db.QueryRowContext(ctx,
			`SELECT method_id FROM wallet_payment_method WHERE qr_file_id=$1 AND enabled=TRUE`, fileID).Scan(&m)
		if err == nil {
			break
		}
		err = s.db.QueryRowContext(ctx,
			`SELECT method_id FROM wallet_withdraw_method WHERE qr_file_id=$1 AND user_id=$2 AND status=0`, fileID, uid).Scan(&m)
		if err != nil {
			return "", "", false
		}
	case uploadKindProof:
		var owner string
		err := s.db.QueryRowContext(ctx,
			`SELECT uploaded_by_user_id FROM recharge_proof WHERE file_id=$1`, fileID).Scan(&owner)
		if err != nil || owner != uid {
			return "", "", false
		}
	default:
		return "", "", false
	}
	return contentType, storagePath, true
}

// hGetUpload：GET /v1/uploads/{file_id}。管理员走 X-Admin-Secret；用户走 IM token。
// 响应为文件字节 + 存储的 Content-Type；鉴权失败统一 404（不透露存在性），无 token 回 401。
func (s *Server) hGetUpload(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/v1/uploads/")
	if id == "" || strings.Contains(id, "/") {
		fail(w, http.StatusNotFound, "NOT_FOUND", "upload file not found")
		return
	}
	if s.authAdmin(r) {
		var contentType, storagePath string
		err := s.db.QueryRowContext(r.Context(),
			`SELECT content_type, storage_path FROM fund_upload_file WHERE file_id=$1`, id).
			Scan(&contentType, &storagePath)
		if err == sql.ErrNoRows {
			fail(w, http.StatusNotFound, "NOT_FOUND", "upload file not found")
			return
		}
		if err != nil {
			fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}
		s.serveUploadFile(w, r, contentType, storagePath)
		return
	}
	uid, okAuth, code, msg := s.authUser(r)
	if !okAuth {
		fail(w, code, "UNAUTHORIZED", msg)
		return
	}
	contentType, storagePath, ok := s.canViewUpload(r.Context(), uid, id)
	if !ok {
		fail(w, http.StatusNotFound, "NOT_FOUND", "upload file not found")
		return
	}
	s.serveUploadFile(w, r, contentType, storagePath)
}

func (s *Server) serveUploadFile(w http.ResponseWriter, r *http.Request, contentType, storagePath string) {
	clean := filepath.Clean(storagePath)
	if !strings.HasPrefix(clean, filepath.Clean(s.uploadDir())+string(os.PathSeparator)) {
		fail(w, http.StatusNotFound, "NOT_FOUND", "upload file not found")
		return
	}
	f, err := os.Open(clean)
	if err != nil {
		fail(w, http.StatusNotFound, "NOT_FOUND", "upload file not found")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		fail(w, http.StatusNotFound, "NOT_FOUND", "upload file not found")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", st.Size()))
	http.ServeContent(w, r, "", st.ModTime(), f)
}

// parseUploadForm：解析 multipart 表单并取指定字段文件。表单体上限 6MB（5MB 文件 + 边界开销）。
// FormFile 打开的文件句柄立即关闭，saveUpload 会用 FileHeader 重新打开读取。
func parseUploadForm(w http.ResponseWriter, r *http.Request, field string) (*multipart.FileHeader, bool) {
	if err := r.ParseMultipartForm(6 << 20); err != nil {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "invalid multipart form: "+err.Error())
		return nil, false
	}
	f, fh, err := r.FormFile(field)
	if err != nil {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "file field "+field+" is required")
		return nil, false
	}
	_ = f.Close()
	return fh, true
}
