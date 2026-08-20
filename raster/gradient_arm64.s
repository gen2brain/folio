//go:build arm64 && !noasm

#include "textflag.h"

#define FADD4S(m, n, d)  WORD $(0x4E20D400 | ((m) << 16) | ((n) << 5) | (d))
#define FSUB4S(m, n, d)  WORD $(0x4EA0D400 | ((m) << 16) | ((n) << 5) | (d))
#define FMUL4S(m, n, d)  WORD $(0x6E20DC00 | ((m) << 16) | ((n) << 5) | (d))
#define FSQRT4S(n, d)    WORD $(0x6EA1F800 | ((n) << 5) | (d))
#define FCMGE4S(m, n, d) WORD $(0x6E20E400 | ((m) << 16) | ((n) << 5) | (d))
#define FCMGEZ4S(n, d)   WORD $(0x6EA0C800 | ((n) << 5) | (d))
#define FMAX4S(m, n, d)  WORD $(0x4E20F400 | ((m) << 16) | ((n) << 5) | (d))
#define FMIN4S(m, n, d)  WORD $(0x4EA0F400 | ((m) << 16) | ((n) << 5) | (d))
#define FCVTZS4S(n, d)   WORD $(0x4EA1B800 | ((n) << 5) | (d))
#define AND16B(m, n, d)  WORD $(0x4E201C00 | ((m) << 16) | ((n) << 5) | (d))
#define ORR16B(m, n, d)  WORD $(0x4EA01C00 | ((m) << 16) | ((n) << 5) | (d))
#define BIC16B(m, n, d)  WORD $(0x4E601C00 | ((m) << 16) | ((n) << 5) | (d))
#define MOVIZ4S(d)       WORD $(0x4F000400 | (d))
#define DUP4S(n, d)      WORD $(0x4E040400 | ((n) << 5) | (d))
#define MVNI4S(d)        WORD $(0x6F000400 | (d))
#define SCVTF4S(n, d)    WORD $(0x4E21D800 | ((n) << 5) | (d))

#define LOAD(off, v)          \
	FMOVS off(R0), F1     \
	DUP4S(1, v)

// func gradRadialNEON(p *gradParams, cu, cv float32, x int, idx *int32, w int)
TEXT ·gradRadialNEON(SB), NOSPLIT, $0-40
	MOVD p+0(FP), R0
	MOVD idx+24(FP), R1
	MOVD w+32(FP), R2
	LSR  $2, R2, R2
	CBZ  R2, done

	LOAD(0, 16)  // ia
	LOAD(4, 17)  // ib
	LOAD(16, 18) // ie
	LOAD(20, 19) // if
	LOAD(24, 20) // x0
	LOAD(28, 21) // y0
	LOAD(32, 24) // dx
	LOAD(36, 25) // dy
	LOAD(40, 30) // r0
	LOAD(44, 31) // dr
	LOAD(48, 28) // a
	LOAD(52, 29) // invA
	LOAD(56, 26) // r0dr
	LOAD(60, 27) // r0sq
	LOAD(68, 15) // sgn
	LOAD(72, 14) // lo
	LOAD(76, 13) // hi

	FMOVS cu+8(FP), F1
	DUP4S(1, 22)
	FMOVS cv+12(FP), F1
	DUP4S(1, 23)

	MOVD $0x3F000000, R3
	VDUP R3, V12.S4
	MOVD $0x437F0000, R3
	VDUP R3, V11.S4
	MOVD $0x40800000, R3
	VDUP R3, V10.S4
	MOVD $0x3F800000, R3
	VDUP R3, V9.S4

	MOVD x+16(FP), R4
	VDUP R4, V0.S4
	MOVD $0, R3
	VMOV R3, V4.S[0]
	MOVD $1, R3
	VMOV R3, V4.S[1]
	MOVD $2, R3
	VMOV R3, V4.S[2]
	MOVD $3, R3
	VMOV R3, V4.S[3]
	VADD V4.S4, V0.S4, V0.S4
	SCVTF4S(0, 0)
	FADD4S(12, 0, 0)

loop:
	FMUL4S(16, 0, 1)
	FADD4S(22, 1, 1)
	FADD4S(18, 1, 1)
	FSUB4S(20, 1, 1)

	FMUL4S(17, 0, 2)
	FADD4S(23, 2, 2)
	FADD4S(19, 2, 2)
	FSUB4S(21, 2, 2)

	FMUL4S(24, 1, 3)
	FMUL4S(25, 2, 4)
	FADD4S(4, 3, 3)
	FADD4S(26, 3, 3)

	FMUL4S(1, 1, 1)
	FMUL4S(2, 2, 2)
	FADD4S(2, 1, 1)
	FSUB4S(27, 1, 1)

	FMUL4S(3, 3, 5)
	FMUL4S(28, 1, 1)
	FSUB4S(1, 5, 5)
	FSQRT4S(5, 5)
	FMUL4S(15, 5, 5)

	FADD4S(5, 3, 6)
	FMUL4S(29, 6, 6)
	FSUB4S(5, 3, 3)
	FMUL4S(29, 3, 3)

	FMUL4S(31, 6, 8)
	FADD4S(30, 8, 8)
	FCMGEZ4S(8, 1)
	FCMGE4S(14, 6, 4)
	AND16B(4, 1, 1)
	FCMGE4S(6, 13, 4)
	AND16B(4, 1, 1)

	FMUL4S(31, 3, 8)
	FADD4S(30, 8, 8)
	FCMGEZ4S(8, 2)
	FCMGE4S(14, 3, 4)
	AND16B(4, 2, 2)
	FCMGE4S(3, 13, 4)
	AND16B(4, 2, 2)

	AND16B(1, 6, 6)
	BIC16B(1, 3, 4)
	ORR16B(4, 6, 6)
	ORR16B(2, 1, 1)

	MOVIZ4S(4)
	FMAX4S(4, 6, 6)
	FMIN4S(9, 6, 6)

	FMUL4S(11, 6, 6)
	FADD4S(12, 6, 6)
	FCVTZS4S(6, 6)

	MVNI4S(4)
	AND16B(1, 6, 6)
	BIC16B(1, 4, 4)
	ORR16B(4, 6, 6)

	VST1.P [V6.B16], 16(R1)

	FADD4S(10, 0, 0)
	SUB  $1, R2, R2
	CBNZ R2, loop

done:
	RET
