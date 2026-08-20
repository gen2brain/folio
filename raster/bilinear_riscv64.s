//go:build riscv64 && riscv64.rva23u64 && !noasm

#include "textflag.h"

#define BLEND(A0, A1, R)      \
	VWADDUVX  X19, V2, V4 \
	VWMACCUVV A0, V18, V4 \
	VWMACCUVV A1, V16, V4 \
	VNSRLWI   $16, V4, R

#define HEAD                                  \
	VSETVLI     X12, E32, M2, TA, MA, X16 \
	VIDV        V8                        \
	VADDVX      X15, V8, V8               \
	VFCVTFXV    V8, V8                    \
	VFADDVF     F3, V8, V8                \
	VFMULVF     F0, V8, V8                \
	VFADDVF     F1, V8, V8                \
	VFADDVF     F2, V8, V8                \
	VFADDVF     F4, V8, V8                \
	VFCVTRTZXFV V8, V10                   \
	VFCVTFXV    V10, V12                  \
	VMFLTVV     V12, V8, V0               \
	VADDVI      $-1, V10, V0, V10         \
	VFCVTFXV    V10, V12                  \
	VFSUBVV     V12, V8, V14              \
	VFMULVF     F5, V14, V14              \
	VFCVTRTZXFV V14, V14                  \
	VSUBVX      X14, V10, V10             \
	VMULVX      X17, V10, V10             \
	VSETVLI     X12, E16, M1, TA, MA, X16 \
	VNSRLWI     $0, V14, V16              \
	VRSUBVX     X18, V16, V18             \
	VMVVI       $0, V2

#define TAIL              \
	MUL X13, X16, X20 \
	ADD X20, X10, X10 \
	ADD X16, X15, X15 \
	SUB X16, X12, X12

// func bilinearSpanRVV(dst *byte, col *uint16, w, n, k0, x int, a, cu, e float32)
TEXT ·bilinearSpanRVV(SB), NOSPLIT, $0-60
	MOV  dst+0(FP), X10
	MOV  col+8(FP), X11
	MOV  w+16(FP), X12
	MOV  n+24(FP), X13
	MOV  k0+32(FP), X14
	MOV  x+40(FP), X15
	MOVF a+48(FP), F0
	MOVF cu+52(FP), F1
	MOVF e+56(FP), F2

	MOV   $0x3F000000, X20
	FMVWX X20, F3
	MOV   $0xBF000000, X20
	FMVWX X20, F4
	MOV   $0x43800000, X20
	FMVWX X20, F5

	MOV  X13, X17
	SLLI $1, X17, X17
	MOV  $256, X18
	MOV  $32768, X19

	MOV  $1, X21
	BEQ  X13, X21, wide1
	MOV  $2, X21
	BEQ  X13, X21, wide2
	MOV  $3, X21
	BEQ  X13, X21, wide3

wide4:
	HEAD
	VLUXSEG8EI32V (X11), V10, V20
	BLEND(V20, V24, V28)
	BLEND(V21, V25, V29)
	BLEND(V22, V26, V30)
	BLEND(V23, V27, V31)
	VSETVLI X12, E8, MF2, TA, MA, X16
	VNSRLWI $0, V28, V20
	VNSRLWI $0, V29, V21
	VNSRLWI $0, V30, V22
	VNSRLWI $0, V31, V23
	VSSEG4E8V V20, (X10)
	TAIL
	BNE X0, X12, wide4
	RET

wide3:
	HEAD
	VLUXSEG6EI32V (X11), V10, V20
	BLEND(V20, V23, V28)
	BLEND(V21, V24, V29)
	BLEND(V22, V25, V30)
	VSETVLI X12, E8, MF2, TA, MA, X16
	VNSRLWI $0, V28, V20
	VNSRLWI $0, V29, V21
	VNSRLWI $0, V30, V22
	VSSEG3E8V V20, (X10)
	TAIL
	BNE X0, X12, wide3
	RET

wide2:
	HEAD
	VLUXSEG4EI32V (X11), V10, V20
	BLEND(V20, V22, V28)
	BLEND(V21, V23, V29)
	VSETVLI X12, E8, MF2, TA, MA, X16
	VNSRLWI $0, V28, V20
	VNSRLWI $0, V29, V21
	VSSEG2E8V V20, (X10)
	TAIL
	BNE X0, X12, wide2
	RET

wide1:
	HEAD
	VLUXSEG2EI32V (X11), V10, V20
	BLEND(V20, V21, V28)
	VSETVLI X12, E8, MF2, TA, MA, X16
	VNSRLWI $0, V28, V20
	VSE8V   V20, (X10)
	TAIL
	BNE X0, X12, wide1
	RET
