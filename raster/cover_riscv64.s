//go:build riscv64 && riscv64.rva23u64 && !noasm

#include "textflag.h"

#define COMP(D)              \
	MOVBU     (X19), X20 \
	ADD       $1, X19, X19 \
	VWADDUVX  X16, V14, V24 \
	VWMACCUVX V10, X20, V24 \
	VWMACCUVV D, V12, V24 \
	VNSRLWI   $8, V24, V20 \
	VWADDUWV  V20, V24, V24 \
	VNSRLWI   $8, V24, D

// func coverBlendRVV(dst, src, cover *byte, w, n int)
TEXT ·coverBlendRVV(SB), NOSPLIT, $0-40
	MOV dst+0(FP), X10
	MOV src+8(FP), X11
	MOV cover+16(FP), X12
	MOV w+24(FP), X13
	MOV n+32(FP), X14

	MOV $128, X16

	MOV  $1, X22
	BEQ  X14, X22, wide1
	MOV  $2, X22
	BEQ  X14, X22, wide2
	MOV  $3, X22
	BEQ  X14, X22, wide3
	MOV  $4, X22
	BEQ  X14, X22, wide4
	JMP  wide5

#define HEAD                                 \
	VSETVLI X13, E8, M2, TA, MA, X15 \
	VLE8V   (X12), V10               \
	VXORVI  $-1, V10, V12            \
	VMVVI   $0, V14                  \
	MOV     X11, X19

#define HEAD1                                \
	VSETVLI X13, E8, M1, TA, MA, X15 \
	VLE8V   (X12), V10               \
	VXORVI  $-1, V10, V12            \
	VMVVI   $0, V14                  \
	MOV     X11, X19

#define TAIL                     \
	MUL X14, X15, X21        \
	ADD X21, X10, X10        \
	ADD X15, X12, X12        \
	SUB X15, X13, X13

wide1:
	HEAD
	VLE8V (X10), V0
	COMP(V0)
	VSE8V V0, (X10)
	TAIL
	BNE X0, X13, wide1
	RET

wide2:
	HEAD
	VLSEG2E8V (X10), V0
	COMP(V0)
	COMP(V2)
	VSSEG2E8V V0, (X10)
	TAIL
	BNE X0, X13, wide2
	RET

wide3:
	HEAD
	VLSEG3E8V (X10), V0
	COMP(V0)
	COMP(V2)
	COMP(V4)
	VSSEG3E8V V0, (X10)
	TAIL
	BNE X0, X13, wide3
	RET

wide4:
	HEAD
	VLSEG4E8V (X10), V0
	COMP(V0)
	COMP(V2)
	COMP(V4)
	COMP(V6)
	VSSEG4E8V V0, (X10)
	TAIL
	BNE X0, X13, wide4
	RET

wide5:
	HEAD1
	VLSEG5E8V (X10), V0
	COMP(V0)
	COMP(V1)
	COMP(V2)
	COMP(V3)
	COMP(V4)
	VSSEG5E8V V0, (X10)
	TAIL
	BNE X0, X13, wide5
	RET
