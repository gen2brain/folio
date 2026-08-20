//go:build arm64 && !noasm

#include "textflag.h"

#define SCVTF4S(n, d)    WORD $(0x4E21D800 | ((n) << 5) | (d))
#define FCVTZS4S(n, d)   WORD $(0x4EA1B800 | ((n) << 5) | (d))
#define FADD4S(m, n, d)  WORD $(0x4E20D400 | ((m) << 16) | ((n) << 5) | (d))
#define FSUB4S(m, n, d)  WORD $(0x4EA0D400 | ((m) << 16) | ((n) << 5) | (d))
#define FMUL4S(m, n, d)  WORD $(0x6E20DC00 | ((m) << 16) | ((n) << 5) | (d))
#define FCMGT4S(m, n, d) WORD $(0x6EA0E400 | ((m) << 16) | ((n) << 5) | (d))
#define MUL4S(m, n, d)   WORD $(0x4EA09C00 | ((m) << 16) | ((n) << 5) | (d))
#define XTN4H(n, d)      WORD $(0x0E612800 | ((n) << 5) | (d))
#define XTN8B(n, d)      WORD $(0x0E212800 | ((n) << 5) | (d))

#define PIXEL(K, F)                  \
	SUBW  R8, K, R14             \
	MULW  R12, R14, R14          \
	ADD   R1, R14, R14           \
	VLD1  (R14), [V6.B16]        \
	MOVD  $256, R15              \
	SUB   F, R15, R15            \
	VDUP  F, V9.S4               \
	VDUP  R15, V10.S4            \
	VUXTL V6.H4, V7.S4           \
	VTBL  V16.B16, [V6.B16], V8.B16 \
	VUXTL V8.H4, V8.S4           \
	MUL4S(10, 7, 7)              \
	MUL4S(9, 8, 8)               \
	VADD  V8.S4, V7.S4, V7.S4    \
	VADD  V17.S4, V7.S4, V7.S4   \
	VUSHR $16, V7.S4, V7.S4      \
	XTN4H(7, 7)                  \
	XTN8B(7, 7)                  \
	VMOV  V7.S[0], R15           \
	MOVW  R15, (R0)              \
	ADD   R11, R0, R0

// func bilinearSpanNEON(dst *byte, col *uint16, idx *byte, w, n, k0, x int, a, cu, e float32)
TEXT ·bilinearSpanNEON(SB), NOSPLIT, $0-68
	MOVD  dst+0(FP), R0
	MOVD  col+8(FP), R1
	MOVD  idx+16(FP), R2
	MOVD  w+24(FP), R3
	MOVD  n+32(FP), R11
	MOVD  k0+40(FP), R8
	MOVD  x+48(FP), R9
	FMOVS a+56(FP), F0
	FMOVS cu+60(FP), F1
	FMOVS e+64(FP), F2

	VDUP V0.S[0], V20.S4
	VDUP V1.S[0], V21.S4
	VDUP V2.S[0], V22.S4

	VLD1 (R2), [V16.B16]

	MOVD  $0x3F000000, R10
	VDUP  R10, V23.S4
	MOVD  $0x43800000, R10
	VDUP  R10, V24.S4
	MOVD  $32768, R10
	VDUP  R10, V17.S4

	VDUP R9, V0.S4
	VMOVQ $0x0000000100000000, $0x0000000300000002, V25
	VADD V25.S4, V0.S4, V0.S4
	MOVD $4, R10
	VDUP R10, V26.S4

	MOVD R11, R12
	LSL  $1, R12, R12

loop:
	SCVTF4S(0, 1)
	FADD4S(23, 1, 1)
	FMUL4S(20, 1, 1)
	FADD4S(21, 1, 1)
	FADD4S(22, 1, 1)
	FSUB4S(23, 1, 1)

	FCVTZS4S(1, 2)
	SCVTF4S(2, 4)
	FCMGT4S(1, 4, 5)
	VADD V5.S4, V2.S4, V2.S4

	SCVTF4S(2, 4)
	FSUB4S(4, 1, 18)
	FMUL4S(24, 18, 18)
	FCVTZS4S(18, 3)

	VMOV V2.S[0], R4
	VMOV V2.S[1], R5
	VMOV V2.S[2], R6
	VMOV V2.S[3], R7
	VMOV V3.S[0], R19
	VMOV V3.S[1], R20
	VMOV V3.S[2], R21
	VMOV V3.S[3], R22

	PIXEL(R4, R19)
	PIXEL(R5, R20)
	PIXEL(R6, R21)
	PIXEL(R7, R22)

	VADD V26.S4, V0.S4, V0.S4
	SUB  $4, R3, R3
	CBNZ R3, loop

	RET
