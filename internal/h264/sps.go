package h264

import "errors"

// Color is the colour description an SPS carries in its VUI.
type Color struct {
	// Described is false when the stream does not say which matrix it uses;
	// players then assume BT.601 for SD sizes and BT.709 for HD.
	Described bool
	Matrix    int // matrix_coefficients: 1 BT.709, 5 or 6 BT.601
	FullRange bool
}

// StreamColor finds the first SPS in an Annex B frame and returns its
// colour description. ok is false when the frame has no SPS.
func StreamColor(frame []byte) (c Color, ok bool) {
	for nal := range nalUnits(frame) {
		if len(nal) > 1 && nal[0]&0x1f == 7 {
			c, err := parseSPSColor(unescape(nal[1:]))
			return c, err == nil
		}
	}
	return Color{}, false
}

// nalUnits yields the NAL units of an Annex B byte stream.
func nalUnits(b []byte) func(func([]byte) bool) {
	return func(yield func([]byte) bool) {
		start := -1
		for i := 0; i+2 < len(b); i++ {
			if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
				if start >= 0 {
					end := i
					if end > start && b[end-1] == 0 {
						end--
					}
					if !yield(b[start:end]) {
						return
					}
				}
				start = i + 3
				i += 2
			}
		}
		if start >= 0 && start < len(b) {
			yield(b[start:])
		}
	}
}

// unescape removes emulation prevention bytes (00 00 03).
func unescape(b []byte) []byte {
	out := make([]byte, 0, len(b))
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c == 3 {
			zeros = 0
			continue
		}
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, c)
	}
	return out
}

type bits struct {
	b   []byte
	pos int
	err error
}

func (r *bits) bit() uint {
	if r.pos >= len(r.b)*8 {
		r.err = errors.New("h264: SPS too short")
		return 0
	}
	v := uint(r.b[r.pos/8]>>(7-r.pos%8)) & 1
	r.pos++
	return v
}

func (r *bits) u(n int) uint {
	var v uint
	for range n {
		v = v<<1 | r.bit()
	}
	return v
}

func (r *bits) ue() uint {
	zeros := 0
	for r.bit() == 0 && r.err == nil {
		zeros++
		if zeros > 31 {
			r.err = errors.New("h264: bad exp-Golomb code")
			return 0
		}
	}
	return 1<<zeros - 1 + r.u(zeros)
}

func (r *bits) se() int {
	v := r.ue()
	if v&1 != 0 {
		return int(v+1) / 2
	}
	return -int(v / 2)
}

func parseSPSColor(sps []byte) (Color, error) {
	r := &bits{b: sps}
	profile := r.u(8)
	r.u(16) // constraint flags, level
	r.ue()  // seq_parameter_set_id
	switch profile {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		chroma := r.ue()
		if chroma == 3 {
			r.bit() // separate_colour_plane_flag
		}
		r.ue()            // bit_depth_luma_minus8
		r.ue()            // bit_depth_chroma_minus8
		r.bit()           // qpprime_y_zero_transform_bypass_flag
		if r.bit() == 1 { // seq_scaling_matrix_present_flag
			lists := 8
			if chroma == 3 {
				lists = 12
			}
			for i := range lists {
				if r.bit() == 0 {
					continue
				}
				size := 16
				if i >= 6 {
					size = 64
				}
				last, next := 8, 8
				for range size {
					if next != 0 {
						next = (last + r.se() + 256) % 256
					}
					if next != 0 {
						last = next
					}
				}
			}
		}
	}
	r.ue() // log2_max_frame_num_minus4
	switch r.ue() {
	case 0:
		r.ue() // log2_max_pic_order_cnt_lsb_minus4
	case 1:
		r.bit()
		r.se()
		r.se()
		for range r.ue() {
			r.se()
		}
	}
	r.ue()            // max_num_ref_frames
	r.bit()           // gaps_in_frame_num_value_allowed_flag
	r.ue()            // pic_width_in_mbs_minus1
	r.ue()            // pic_height_in_map_units_minus1
	if r.bit() == 0 { // frame_mbs_only_flag
		r.bit()
	}
	r.bit()           // direct_8x8_inference_flag
	if r.bit() == 1 { // frame_cropping_flag
		r.ue()
		r.ue()
		r.ue()
		r.ue()
	}
	var c Color
	if r.bit() == 1 { // vui_parameters_present_flag
		if r.bit() == 1 { // aspect_ratio_info_present_flag
			if r.u(8) == 255 {
				r.u(32)
			}
		}
		if r.bit() == 1 { // overscan_info_present_flag
			r.bit()
		}
		if r.bit() == 1 { // video_signal_type_present_flag
			r.u(3) // video_format
			c.FullRange = r.bit() == 1
			if r.bit() == 1 { // colour_description_present_flag
				r.u(8) // colour_primaries
				r.u(8) // transfer_characteristics
				c.Matrix = int(r.u(8))
				c.Described = c.Matrix != 2 // 2 means unspecified
			}
		}
	}
	return c, r.err
}
