package hexstring

func char_to_byte(c byte) int {
	if (c >= '0' && c <= '9') {
		return int(c - '0')
	}
	if (c >= 'A' && c <= 'F') {
		return int(c - 'A') + 10
	}
	if (c >= 'a' && c <= 'f') {
		return int(c - 'a') + 10
	}
	return -1
}

/*
 * encode the hexstring into bytes, ignoring all non-HEX characters
 * returns the length of the encoded bytes, or -1 on error (buffer too small)
 */
func Encode(buf []byte, str string) int {
	var (
		i, j, lenstr, lenbuf int
		shift bool = true
	)
	lenstr = len(str)
	lenbuf = len(buf)

	for i = 0; i < lenstr; i++ {
		var c int = char_to_byte(str[i])
		if (c < 0) {
			continue;
		}
		if (shift) {
			if (j >= lenbuf) {
				return -1
			}
			buf[j] = byte(c << 4)
		} else {
			buf[j] |= byte(c)
			j++
		}
		shift = !shift
	}
	return j
}

/*
var nibble [16]Rune = { '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', 'a', 'b', 'c', 'd', 'e', 'f' }

func byte_to_str(b byte) string {
	var (
		high int = b >> 4
		low int = b & 0x0f
	)
	return string(nibble[high]) + string(nibble[low])
}
*/
