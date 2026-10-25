package xengine

// util.go 小工具: hex 解析等。

import "strconv"

// strconvParseHex 解析 16 进制串(uint64)。
func strconvParseHex(s string) (int64, error) {
	v, err := strconv.ParseUint(s, 16, 64)
	if err != nil {
		return 0, err
	}
	return int64(v), nil
}
