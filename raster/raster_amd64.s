//go:build amd64 && !noasm

#include "textflag.h"

TEXT ·cpuidAVX2(SB), NOSPLIT, $0-1
	MOVL $0, AX
	CPUID
	CMPL AX, $7
	JL   no_avx2

	MOVL $1, AX
	MOVL $0, CX
	CPUID
	BTL  $27, CX
	JNC  no_avx2

	MOVL $0, CX
	XGETBV
	ANDL $6, AX
	CMPL AX, $6
	JNE  no_avx2

	MOVL $7, AX
	MOVL $0, CX
	CPUID
	BTL  $5, BX
	JNC  no_avx2

	MOVB $1, ret+0(FP)
	RET

no_avx2:
	MOVB $0, ret+0(FP)
	RET

TEXT ·cpuidSSE41(SB), NOSPLIT, $0-1
	MOVL $1, AX
	MOVL $0, CX
	CPUID
	ANDL $0x00080200, CX
	CMPL CX, $0x00080200
	JNE  no_sse

	MOVB $1, ret+0(FP)
	RET

no_sse:
	MOVB $0, ret+0(FP)
	RET
