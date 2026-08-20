//go:build arm64 && !noasm

#include "textflag.h"

#define UMULL(m, n, d)   WORD $(0x2E20C000 | ((m) << 16) | ((n) << 5) | (d))
#define UMULL2(m, n, d)  WORD $(0x6E20C000 | ((m) << 16) | ((n) << 5) | (d))
#define UMLAL(m, n, d)   WORD $(0x2E208000 | ((m) << 16) | ((n) << 5) | (d))
#define UMLAL2(m, n, d)  WORD $(0x6E208000 | ((m) << 16) | ((n) << 5) | (d))

// func coverBlendNEON(dst, src, cover, idxa, idxs *byte, m, step, gn int)
TEXT ·coverBlendNEON(SB), NOSPLIT, $0-64
	MOVD dst+0(FP), R0
	MOVD src+8(FP), R1
	MOVD cover+16(FP), R2
	MOVD idxa+24(FP), R3
	MOVD idxs+32(FP), R4
	MOVD m+40(FP), R5
	MOVD step+48(FP), R6
	MOVD gn+56(FP), R7

	VLD1 (R1), [V28.B16]
	VLD1 (R3), [V26.B16]
	VLD1 (R4), [V27.B16]

	MOVD $128, R10
	VDUP R10, V30.H8
	MOVD $255, R10
	VDUP R10, V29.B16

loop:
	CMP  $16, R6
	BEQ  ld16
	CMP  $8, R6
	BEQ  ld8
	CMP  $4, R6
	BEQ  ld4
	MOVHU (R2), R11
	VMOV  R11, V0.D[0]
	B     ldone

ld4:
	MOVWU (R2), R11
	VMOV  R11, V0.D[0]
	B     ldone

ld8:
	MOVD (R2), R11
	VMOV R11, V0.D[0]
	B    ldone

ld16:
	VLD1 (R2), [V0.B16]

ldone:
	VLD1 (R0), [V3.B16]

	VTBL V26.B16, [V0.B16], V4.B16
	VTBL V27.B16, [V28.B16], V5.B16
	VEOR V29.B16, V4.B16, V6.B16

	UMULL(4, 5, 16)
	UMULL2(4, 5, 17)
	UMLAL(6, 3, 16)
	UMLAL2(6, 3, 17)

	VADD  V30.H8, V16.H8, V16.H8
	VADD  V30.H8, V17.H8, V17.H8
	VUSHR $8, V16.H8, V18.H8
	VUSHR $8, V17.H8, V19.H8
	VADD  V18.H8, V16.H8, V16.H8
	VADD  V19.H8, V17.H8, V17.H8
	VUSHR $8, V16.H8, V16.H8
	VUSHR $8, V17.H8, V17.H8

	VUZP1 V17.B16, V16.B16, V20.B16
	VST1  [V20.B16], (R0)

	ADD  R7, R0, R0
	ADD  R6, R2, R2
	SUB  $1, R5, R5
	CBNZ R5, loop

	RET
