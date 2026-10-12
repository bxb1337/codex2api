package security

import "io"

// 长度只作为容量提示；继续读到 EOF，保留超限、额外字节和读取错误检查。
func readRequestMemoryKnownLength(reader io.Reader, size int64) ([]byte, error) {
	body := make([]byte, 0, size)
	var probe [1]byte
	for {
		var n int
		var err error
		if len(body) == cap(body) {
			// 满容量后先探测 EOF，避免正文恰好读完时仍扩容整个缓冲。
			n, err = reader.Read(probe[:])
			body = append(body, probe[:n]...)
		} else {
			n, err = reader.Read(body[len(body):cap(body)])
			body = body[:len(body)+n]
		}
		if err != nil {
			if err == io.EOF {
				err = nil
			}
			return body, err
		}
	}
}
