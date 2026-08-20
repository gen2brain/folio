//go:build riscv64 && riscv64.rva23u64 && !noasm

#include "textflag.h"

// func blendRowsRVV(dst *uint16, a, c *byte, n int, iv, fv uint32)
TEXT ·blendRowsRVV(SB), NOSPLIT, $0-40
	MOV  dst+0(FP), X10
	MOV  a+8(FP), X11
	MOV  c+16(FP), X12
	MOV  n+24(FP), X13
	MOVWU iv+32(FP), X14
	MOVWU fv+36(FP), X15

loop:
	VSETVLI X13, E16, M4, TA, MA, X16
	VLE8V   (X11), V0
	VLE8V   (X12), V4

	VZEXTVF2 V0, V8
	VZEXTVF2 V4, V12
	VMULVX   X14, V8, V8
	VMACCVX  V12, X15, V8

	VSE16V V8, (X10)

	SLLI $1, X16, X17
	ADD  X17, X10, X10
	ADD  X16, X11, X11
	ADD  X16, X12, X12
	SUB  X16, X13, X13
	BNE  X0, X13, loop

	RET
