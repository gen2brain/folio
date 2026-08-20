//go:build amd64 && !noasm

#include "textflag.h"

DATA bilinearSeq+0(SB)/4, $0
DATA bilinearSeq+4(SB)/4, $1
DATA bilinearSeq+8(SB)/4, $2
DATA bilinearSeq+12(SB)/4, $3
GLOBL bilinearSeq(SB), RODATA|NOPTR, $16

DATA bilinearFour+0(SB)/4, $4
DATA bilinearFour+4(SB)/4, $4
DATA bilinearFour+8(SB)/4, $4
DATA bilinearFour+12(SB)/4, $4
GLOBL bilinearFour(SB), RODATA|NOPTR, $16

#define PIXEL(KS, FS)               \
	MOVL     KS(SP), AX         \
	SUBL     R8, AX             \
	IMULL    R12, AX            \
	MOVL     FS(SP), DX         \
	MOVD     DX, X4             \
	PSHUFD   $0, X4, X4         \
	MOVL     $256, R13          \
	SUBL     DX, R13            \
	MOVD     R13, X5            \
	PSHUFD   $0, X5, X5         \
	MOVOU    (SI)(AX*1), X6     \
	PMOVZXWD X6, X7             \
	PSHUFB   X15, X6            \
	PMOVZXWD X6, X8             \
	PMULLD   X5, X7             \
	PMULLD   X4, X8             \
	PADDL    X8, X7             \
	PADDL    X14, X7            \
	PSRLL    $16, X7            \
	PACKUSDW X7, X7             \
	PACKUSWB X7, X7             \
	MOVL     X7, (DI)           \
	ADDQ     BX, DI

// func bilinearSpanSSE(dst *byte, col *uint16, idx *byte, w, n, k0, x int, a, cu, e float32)
TEXT ·bilinearSpanSSE(SB), NOSPLIT, $32-68
	MOVQ  dst+0(FP), DI
	MOVQ  col+8(FP), SI
	MOVQ  idx+16(FP), R11
	MOVQ  w+24(FP), CX
	MOVQ  n+32(FP), BX
	MOVQ  k0+40(FP), R8
	MOVQ  x+48(FP), R9
	MOVSS a+56(FP), X10
	MOVSS cu+60(FP), X11
	MOVSS e+64(FP), X12

	SHUFPS $0, X10, X10
	SHUFPS $0, X11, X11
	SHUFPS $0, X12, X12

	MOVOU (R11), X15

	MOVL   $0x3F000000, AX
	MOVD   AX, X9
	SHUFPS $0, X9, X9
	MOVL   $0x43800000, AX
	MOVD   AX, X13
	SHUFPS $0, X13, X13
	MOVL   $32768, AX
	MOVD   AX, X14
	PSHUFD $0, X14, X14

	MOVL   R9, AX
	MOVD   AX, X0
	PSHUFD $0, X0, X0
	PADDL  bilinearSeq(SB), X0

	MOVQ BX, R12
	SHLQ $1, R12

loop:
	CVTPL2PS X0, X1
	ADDPS    X9, X1
	MULPS    X10, X1
	ADDPS    X11, X1
	ADDPS    X12, X1
	SUBPS    X9, X1

	CVTTPS2PL X1, X2
	CVTPL2PS  X2, X4
	MOVUPS    X1, X5
	CMPPS     X4, X5, $1
	PADDL     X5, X2

	CVTPL2PS X2, X4
	MOVUPS   X1, X6
	SUBPS    X4, X6
	MULPS    X13, X6
	CVTTPS2PL X6, X3

	MOVOU X2, ka-32(SP)
	MOVOU X3, fa-16(SP)

	PIXEL(ka-32, fa-16)
	PIXEL(kb-28, fb-12)
	PIXEL(kc-24, fc-8)
	PIXEL(kd-20, fd-4)

	PADDL bilinearFour(SB), X0
	SUBQ  $4, CX
	JNZ   loop

	RET
