package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/disintegration/imaging"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	_ "golang.org/x/image/webp"
)

func normalizeImage(raw []byte) ([]byte, string, int, int, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, "", 0, 0, invalid("图片内容无法读取，请使用JPEG、PNG或WebP。")
	}
	if (format != "jpeg" && format != "png" && format != "webp") || cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > 20_000_000 {
		return nil, "", 0, 0, invalid("图片格式不支持或像素过大，请压缩后重试。")
	}
	im, err := imaging.Decode(bytes.NewReader(raw), imaging.AutoOrientation(true))
	if err != nil {
		return nil, "", 0, 0, invalid("图片解码失败。")
	}
	var b bytes.Buffer
	contentType := "image/png"
	if format == "jpeg" {
		err = jpeg.Encode(&b, im, &jpeg.Options{Quality: 90})
		contentType = "image/jpeg"
	} else {
		err = png.Encode(&b, im)
	}
	if err != nil {
		return nil, "", 0, 0, err
	}
	if b.Len() > 16<<20 {
		return nil, "", 0, 0, invalid("图片处理后过大，请先缩小尺寸。")
	}
	return b.Bytes(), contentType, im.Bounds().Dx(), im.Bounds().Dy(), nil
}
func (a *App) uploadAttachment(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 9<<20)
	if err := c.Request.ParseMultipartForm(1 << 20); err != nil {
		fail(c, 400, "invalid_upload", "请上传8MB以内的图片。")
		return
	}
	defer c.Request.MultipartForm.RemoveAll()
	f, h, err := c.Request.FormFile("file")
	if err != nil {
		fail(c, 400, "invalid_upload", "没有收到图片。")
		return
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (8<<20)+1))
	if err != nil || len(raw) > 8<<20 {
		fail(c, 400, "invalid_upload", "单张图片不能超过8MB。")
		return
	}
	b, ctype, w, height, err := normalizeImage(raw)
	if err != nil {
		fail(c, 400, "invalid_image", err.Error())
		return
	}
	user := actor(c).ID
	tx, err := a.DB.Begin(c)
	if err != nil {
		internalError(c, err)
		return
	}
	defer tx.Rollback(c)
	if err = lockHealthWrites(c, tx, user); err != nil {
		internalError(c, err)
		return
	}
	id := uuid.NewString()
	dir := filepath.Join(a.Config.StorageDir, user)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		internalError(c, err)
		return
	}
	path := filepath.Join(dir, id)
	if err = os.WriteFile(path, b, 0o600); err != nil {
		internalError(c, err)
		return
	}
	sum := sha256.Sum256(b)
	name := filepath.Base(strings.ReplaceAll(h.Filename, "\\", "/"))
	if len(name) > 200 {
		name = "image"
	}
	_, err = tx.Exec(c, "INSERT INTO attachments(id,user_id,filename,content_type,size_bytes,sha256,width,height) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", id, user, name, ctype, len(b), hex.EncodeToString(sum[:]), w, height)
	if err != nil {
		_ = os.Remove(path)
		internalError(c, err)
		return
	}
	if err = tx.Commit(c); err != nil {
		_ = os.Remove(path)
		internalError(c, err)
		return
	}
	c.JSON(201, gin.H{"id": id, "filename": name, "content_type": ctype, "size_bytes": len(b), "width": w, "height": height, "metadata_removed": true})
}
func (a *App) getAttachment(c *gin.Context) {
	if !validID(c, "id") {
		return
	}
	var ctype string
	err := a.DB.QueryRow(c, "SELECT content_type FROM attachments WHERE id=$1 AND user_id=$2", c.Param("id"), actor(c).ID).Scan(&ctype)
	if err != nil {
		fail(c, 404, "not_found", "没有找到图片。")
		return
	}
	c.Header("Content-Type", ctype)
	c.Header("Content-Disposition", "inline")
	c.File(filepath.Join(a.Config.StorageDir, actor(c).ID, c.Param("id")))
}

// Storage names are server-generated UUIDs, never client paths.
func (a *App) removeUserFiles(user string, ids []string) error {
	if _, err := uuid.Parse(user); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := uuid.Parse(id); err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(a.Config.StorageDir, user, id)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
