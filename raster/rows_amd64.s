//go:build amd64 && !noasm

#include "textflag.h"

// func blendRowsSSE(dst *uint16, a, c *byte, n int, iv, fv uint32)
TEXT ·blendRowsSSE(SB), NOSPLIT, $0-40
	MOVQ dst+0(FP), DI
	MOVQ a+8(FP), SI
	MOVQ c+16(FP), DX
	MOVQ n+24(FP), CX

	MOVL    iv+32(FP), AX
	MOVD    AX, X6
	PSHUFLW $0, X6, X6
	PSHUFD  $0, X6, X6
	MOVL    fv+36(FP), AX
	MOVD    AX, X7
	PSHUFLW $0, X7, X7
	PSHUFD  $0, X7, X7

loop:
	MOVOU (SI), X0
	MOVOU (DX), X1

	PMOVZXBW X0, X2
	PMOVZXBW X1, X3
	PSRLO    $8, X0
	PSRLO    $8, X1
	PMOVZXBW X0, X4
	PMOVZXBW X1, X5

	PMULLW X6, X2
	PMULLW X7, X3
	PADDW  X3, X2
	PMULLW X6, X4
	PMULLW X7, X5
	PADDW  X5, X4

	MOVOU X2, (DI)
	MOVOU X4, 16(DI)

	ADDQ $16, SI
	ADDQ $16, DX
	ADDQ $32, DI
	SUBQ $16, CX
	JNZ  loop

	RET
