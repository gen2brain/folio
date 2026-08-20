//go:build arm64 && !noasm

#include "textflag.h"

#define MUL8H(m, n, d)   WORD $(0x4E609C00 | ((m) << 16) | ((n) << 5) | (d))
#define MLA8H(m, n, d)   WORD $(0x4E609400 | ((m) << 16) | ((n) << 5) | (d))
#define UXTL8H(n, d)     WORD $(0x2F08A400 | ((n) << 5) | (d))
#define UXTL28H(n, d)    WORD $(0x6F08A400 | ((n) << 5) | (d))

// func blendRowsNEON(dst *uint16, a, c *byte, n int, iv, fv uint32)
TEXT ·blendRowsNEON(SB), NOSPLIT, $0-40
	MOVD dst+0(FP), R0
	MOVD a+8(FP), R1
	MOVD c+16(FP), R2
	MOVD n+24(FP), R3

	MOVWU iv+32(FP), R4
	VDUP  R4, V6.H8
	MOVWU fv+36(FP), R4
	VDUP  R4, V7.H8

loop:
	VLD1.P 16(R1), [V0.B16]
	VLD1.P 16(R2), [V1.B16]

	UXTL8H(0, 2)
	UXTL8H(1, 8)
	UXTL28H(0, 3)
	UXTL28H(1, 9)

	MUL8H(6, 2, 2)
	MLA8H(7, 8, 2)
	MUL8H(6, 3, 3)
	MLA8H(7, 9, 3)

	VST1.P [V2.B16, V3.B16], 32(R0)

	SUB  $16, R3, R3
	CBNZ R3, loop

	RET
