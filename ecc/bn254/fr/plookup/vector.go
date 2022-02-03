// Copyright 2020 ConsenSys Software Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package plookup

import (
	"crypto/sha256"
	"errors"
	"math/big"
	"math/bits"
	"sort"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr/fft"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr/kzg"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr/polynomial"
	fiatshamir "github.com/consensys/gnark-crypto/fiat-shamir"
)

var (
	ErrNotInTable          = errors.New("some value in the vector is not in the lookup table")
	ErrPlookupVerification = errors.New("plookup verification failed")
)

type Table []fr.Element

// Len is the number of elements in the collection.
func (t Table) Len() int {
	return len(t)
}

// Less reports whether the element with
// index i should sort before the element with index j.
func (t Table) Less(i, j int) bool {
	return t[i].Cmp(&t[j]) == -1
}

// Swap swaps the elements with indexes i and j.
func (t Table) Swap(i, j int) {
	t[i], t[j] = t[j], t[i]
}

// Proof Plookup proof, containing opening proofs
type ProofLookupVector struct {

	// size of the system
	size uint64

	// Commitments to h1, h2, t, z, f, h
	h1, h2, t, z, f, h kzg.Digest

	// Batch opening proof of h1, h2, z, t
	BatchedProof kzg.BatchOpeningProof

	// Batch opening proof of h1, h2, z shifted by g
	BatchedProofShifted kzg.BatchOpeningProof
}

// evaluateAccumulationPolynomial computes Z, in Lagrange basis. Z is the accumulation of the partial
// ratios of 2 fully split polynomials (cf https://eprint.iacr.org/2020/315.pdf)
// * lf is the list of values that should be in lt
// * lt is the lookup table
// * lh1, lh2 is lf sorted by lt split in 2 overlapping slices
// * beta, gamma are challenges (Schwartz-zippel: they are the random evaluations point)
func evaluateAccumulationPolynomial(lf, lt, lh1, lh2 []fr.Element, beta, gamma fr.Element) []fr.Element {

	z := make([]fr.Element, len(lt))

	n := len(lt)
	d := make([]fr.Element, n-1)
	var u, c fr.Element
	c.SetOne().
		Add(&c, &beta).
		Mul(&c, &gamma)
	for i := 0; i < n-1; i++ {

		d[i].Mul(&beta, &lh1[i+1]).
			Add(&d[i], &lh1[i]).
			Add(&d[i], &c)

		u.Mul(&beta, &lh2[i+1]).
			Add(&u, &lh2[i]).
			Add(&u, &c)

		d[i].Mul(&d[i], &u)
	}
	d = fr.BatchInvert(d)

	z[0].SetOne()
	var a, b, e fr.Element
	e.SetOne().Add(&e, &beta)
	for i := 0; i < n-1; i++ {

		a.Add(&gamma, &lf[i])

		b.Mul(&beta, &lt[i+1]).
			Add(&b, &lt[i]).
			Add(&b, &c)

		a.Mul(&a, &b).
			Mul(&a, &e)

		z[i+1].Mul(&z[i], &a).
			Mul(&z[i+1], &d[i])
	}

	return z
}

// evaluateNumBitReversed computes the evaluation (shifted, bit reversed) of h where
// h = (x-g**(n-1))*z*(1+beta)*(gamma+f)*(gamma(1+beta) + t+ beta*t(gX)) -
//		(x-(g**(n-1))*z(gX)*(gamma(1+beta) + h1 + beta*h1(gX))*(gamma(1+beta) + h2 + beta*h2(gX) )
//
// * cz, ch1, ch2, ct, cf are the polynomials z, h1, h2, t, f in canonical basis
// * _lz, _lh1, _lh2, _lt, _lf are the polynomials z, h1, h2, t, f in shifted Lagrange basis (domainBig)
// * beta, gamma are the challenges
// * it returns h in canonical basis
func evaluateNumBitReversed(_lz, _lh1, _lh2, _lt, _lf []fr.Element, beta, gamma fr.Element, domainBig *fft.Domain) []fr.Element {

	// result
	s := int(domainBig.Cardinality)
	num := make([]fr.Element, domainBig.Cardinality)

	var u, onePlusBeta, GammaTimesOnePlusBeta, m, n, one fr.Element

	one.SetOne()
	onePlusBeta.Add(&one, &beta)
	GammaTimesOnePlusBeta.Mul(&onePlusBeta, &gamma)

	g := make([]fr.Element, s)
	g[0].Set(&domainBig.FrMultiplicativeGen)
	for i := 1; i < s; i++ {
		g[i].Mul(&g[i-1], &domainBig.Generator)
	}

	var gg fr.Element
	expo := big.NewInt(int64(domainBig.Cardinality>>1 - 1))
	gg.Square(&domainBig.Generator).Exp(gg, expo)

	nn := uint64(64 - bits.TrailingZeros64(domainBig.Cardinality))

	for i := 0; i < s; i++ {

		_i := int(bits.Reverse64(uint64(i)) >> nn)
		_is := int(bits.Reverse64(uint64((i+2)%s)) >> nn)

		// m = z*(1+beta)*(gamma+f)*(gamma(1+beta) + t+ beta*t(gX))
		m.Mul(&onePlusBeta, &_lz[_i])
		u.Add(&gamma, &_lf[_i])
		m.Mul(&m, &u)
		u.Mul(&beta, &_lt[_is]).
			Add(&u, &_lt[_i]).
			Add(&u, &GammaTimesOnePlusBeta)
		m.Mul(&m, &u)

		// n = z(gX)*(gamma(1+beta) + h1 + beta*h1(gX))*(gamma(1+beta) + h2 + beta*h2(gX)
		n.Mul(&beta, &_lh1[_is]).
			Add(&n, &_lh1[_i]).
			Add(&n, &GammaTimesOnePlusBeta)
		u.Mul(&beta, &_lh2[_is]).
			Add(&u, &_lh2[_i]).
			Add(&u, &GammaTimesOnePlusBeta)
		n.Mul(&n, &u).
			Mul(&n, &_lz[_is])

		// (x-gg**(n-1))*(m-n)
		num[_i].Sub(&m, &n)
		u.Sub(&g[i], &gg)
		num[_i].Mul(&num[_i], &u)

	}

	return num
}

// evaluateXnMinusOneDomainBig returns the evaluation of (x**n-1) on FrMultiplicativeGen*< g  >
func evaluateXnMinusOneDomainBig(domainBig *fft.Domain) [2]fr.Element {

	sizeDomainSmall := domainBig.Cardinality / 2

	var one fr.Element
	one.SetOne()

	// x**n-1 on FrMultiplicativeGen*< g  >
	var res [2]fr.Element
	var shift fr.Element
	shift.Exp(domainBig.FrMultiplicativeGen, big.NewInt(int64(sizeDomainSmall)))
	res[0].Sub(&shift, &one)
	res[1].Add(&shift, &one).Neg(&res[1])

	return res

}

// evaluateL0DomainBig returns the evaluation of (x**n-1)/(x-1) on
// x**n-1 on FrMultiplicativeGen*< g  >
func evaluateL0DomainBig(domainBig *fft.Domain) ([2]fr.Element, []fr.Element) {

	var one fr.Element
	one.SetOne()

	// x**n-1 on FrMultiplicativeGen*< g  >
	xnMinusOne := evaluateXnMinusOneDomainBig(domainBig)

	// 1/(x-1) on FrMultiplicativeGen*< g  >
	var acc fr.Element
	denL0 := make([]fr.Element, domainBig.Cardinality)
	acc.Set(&domainBig.FrMultiplicativeGen)
	for i := 0; i < int(domainBig.Cardinality); i++ {
		denL0[i].Sub(&acc, &one)
		acc.Mul(&acc, &domainBig.Generator)
	}
	denL0 = fr.BatchInvert(denL0)

	return xnMinusOne, denL0
}

// evaluationLnDomainBig returns the evaluation of (x**n-1)/(x-g**(n-1)) on
// x**n-1 on FrMultiplicativeGen*< g  >
func evaluationLnDomainBig(domainBig *fft.Domain) ([2]fr.Element, []fr.Element) {

	sizeDomainSmall := domainBig.Cardinality / 2

	var one fr.Element
	one.SetOne()

	// x**n-1 on FrMultiplicativeGen*< g  >
	numLn := evaluateXnMinusOneDomainBig(domainBig)

	// 1/(x-g**(n-1)) on FrMultiplicativeGen*< g  >
	var gg, acc fr.Element
	gg.Square(&domainBig.Generator).Exp(gg, big.NewInt(int64(sizeDomainSmall-1)))
	denLn := make([]fr.Element, domainBig.Cardinality)
	acc.Set(&domainBig.FrMultiplicativeGen)
	for i := 0; i < int(domainBig.Cardinality); i++ {
		denLn[i].Sub(&acc, &gg)
		acc.Mul(&acc, &domainBig.Generator)
	}
	denLn = fr.BatchInvert(denLn)

	return numLn, denLn

}

// evaluateZStartsByOneBitReversed returns l0 * (z-1), in Lagrange basis and bit reversed order
func evaluateZStartsByOneBitReversed(lsZBitReversed []fr.Element, domainBig *fft.Domain) []fr.Element {

	var one fr.Element
	one.SetOne()

	res := make([]fr.Element, domainBig.Cardinality)

	nn := uint64(64 - bits.TrailingZeros64(domainBig.Cardinality))

	xnMinusOne, denL0 := evaluateL0DomainBig(domainBig)

	for i := 0; i < len(lsZBitReversed); i++ {
		_i := int(bits.Reverse64(uint64(i)) >> nn)
		res[_i].Sub(&lsZBitReversed[_i], &one).
			Mul(&res[_i], &xnMinusOne[i%2]).
			Mul(&res[_i], &denL0[i])
	}

	return res
}

// evaluateZEndsByOneBitReversed returns ln * (z-1), in Lagrange basis and bit reversed order
func evaluateZEndsByOneBitReversed(lsZBitReversed []fr.Element, domainBig *fft.Domain) []fr.Element {

	var one fr.Element
	one.SetOne()

	numLn, denLn := evaluationLnDomainBig(domainBig)

	res := make([]fr.Element, len(lsZBitReversed))
	nn := uint64(64 - bits.TrailingZeros64(domainBig.Cardinality))

	for i := 0; i < len(lsZBitReversed); i++ {
		_i := int(bits.Reverse64(uint64(i)) >> nn)
		res[_i].Sub(&lsZBitReversed[_i], &one).
			Mul(&res[_i], &numLn[i%2]).
			Mul(&res[_i], &denLn[i])
	}

	return res
}

// evaluateOverlapH1h2BitReversed returns ln * (h1 - h2(g.x)), in Lagrange basis and bit reversed order
func evaluateOverlapH1h2BitReversed(_lh1, _lh2 []fr.Element, domainBig *fft.Domain) []fr.Element {

	var one fr.Element
	one.SetOne()

	numLn, denLn := evaluationLnDomainBig(domainBig)

	res := make([]fr.Element, len(_lh1))
	nn := uint64(64 - bits.TrailingZeros64(domainBig.Cardinality))

	s := len(_lh1)
	for i := 0; i < s; i++ {

		_i := int(bits.Reverse64(uint64(i)) >> nn)
		_is := int(bits.Reverse64(uint64((i+2)%s)) >> nn)

		res[_i].Sub(&_lh1[_i], &_lh2[_is]).
			Mul(&res[_i], &numLn[i%2]).
			Mul(&res[_i], &denLn[i])
	}

	return res
}

// computeQuotientCanonical computes the full quotient of the plookup protocol.
// * alpha is the challenge to fold the numerator
// * lh, lh0, lhn, lh1h2 are the various pieces of the numerator (Lagrange shifted form, bit reversed order)
// * domainBig fft domain
// It returns the quotient, in canonical basis
func computeQuotientCanonical(alpha fr.Element, lh, lh0, lhn, lh1h2 []fr.Element, domainBig *fft.Domain) []fr.Element {

	sizeDomainBig := int(domainBig.Cardinality)
	res := make([]fr.Element, sizeDomainBig)

	var one fr.Element
	one.SetOne()

	numLn := evaluateXnMinusOneDomainBig(domainBig)
	numLn[0].Inverse(&numLn[0])
	numLn[1].Inverse(&numLn[1])
	nn := uint64(64 - bits.TrailingZeros64(domainBig.Cardinality))

	for i := 0; i < sizeDomainBig; i++ {

		_i := int(bits.Reverse64(uint64(i)) >> nn)

		res[_i].Mul(&lh1h2[_i], &alpha).
			Add(&res[_i], &lhn[_i]).
			Mul(&res[_i], &alpha).
			Add(&res[_i], &lh0[_i]).
			Mul(&res[_i], &alpha).
			Add(&res[_i], &lh[_i]).
			Mul(&res[_i], &numLn[i%2])
	}

	domainBig.FFTInverse(res, fft.DIT, true)

	return res
}

// ProveLookupVector returns proof that the values in f are in t.
//
// /!\IMPORTANT/!\
//
// If the table t is already commited somewhere (which is the normal workflow
// before generating a lookup proof), the commitment needs to be done on the
// table sorted. Otherwise the commitment in proof.t will not be the same as
// the public commitment: it will contain the same values, but permuted.
//
func ProveLookupVector(srs *kzg.SRS, f, t Table) (ProofLookupVector, error) {

	// res
	var proof ProofLookupVector
	var err error

	// hash function used for Fiat Shamir
	hFunc := sha256.New()

	// transcript to derive the challenge
	fs := fiatshamir.NewTranscript(hFunc, "beta", "gamma", "alpha", "nu")

	// create domains
	var domainSmall *fft.Domain
	if len(t) <= len(f) {
		domainSmall = fft.NewDomain(uint64(len(f) + 1))
	} else {
		domainSmall = fft.NewDomain(uint64(len(t)))
	}
	sizeDomainSmall := int(domainSmall.Cardinality)

	// set the size
	proof.size = domainSmall.Cardinality

	// resize f and t
	// note: the last element of lf does not matter
	lf := make([]fr.Element, sizeDomainSmall)
	lt := make([]fr.Element, sizeDomainSmall)
	cf := make([]fr.Element, sizeDomainSmall)
	ct := make([]fr.Element, sizeDomainSmall)
	copy(lt, t)
	copy(lf, f)
	for i := len(f); i < sizeDomainSmall; i++ {
		lf[i] = f[len(f)-1]
	}
	for i := len(t); i < sizeDomainSmall; i++ {
		lt[i] = t[len(t)-1]
	}
	sort.Sort(Table(lt))
	copy(ct, lt)
	copy(cf, lf)
	domainSmall.FFTInverse(ct, fft.DIF)
	domainSmall.FFTInverse(cf, fft.DIF)
	fft.BitReverse(ct)
	fft.BitReverse(cf)
	proof.t, err = kzg.Commit(ct, srs)
	if err != nil {
		return proof, err
	}
	proof.f, err = kzg.Commit(cf, srs)
	if err != nil {
		return proof, err
	}

	// write f sorted by t
	lfSortedByt := make(Table, 2*domainSmall.Cardinality-1)
	copy(lfSortedByt, lt)
	copy(lfSortedByt[domainSmall.Cardinality:], lf)
	sort.Sort(lfSortedByt)

	// compute h1, h2, commit to them
	lh1 := make([]fr.Element, sizeDomainSmall)
	lh2 := make([]fr.Element, sizeDomainSmall)
	ch1 := make([]fr.Element, sizeDomainSmall)
	ch2 := make([]fr.Element, sizeDomainSmall)
	copy(lh1, lfSortedByt[:sizeDomainSmall])
	copy(lh2, lfSortedByt[sizeDomainSmall-1:])

	copy(ch1, lfSortedByt[:sizeDomainSmall])
	copy(ch2, lfSortedByt[sizeDomainSmall-1:])
	domainSmall.FFTInverse(ch1, fft.DIF)
	domainSmall.FFTInverse(ch2, fft.DIF)
	fft.BitReverse(ch1)
	fft.BitReverse(ch2)

	proof.h1, err = kzg.Commit(ch1, srs)
	if err != nil {
		return proof, err
	}
	proof.h2, err = kzg.Commit(ch2, srs)
	if err != nil {
		return proof, err
	}

	// derive beta, gamma
	beta, err := deriveRandomness(&fs, "beta", &proof.t, &proof.f, &proof.h1, &proof.h2)
	if err != nil {
		return proof, err
	}
	gamma, err := deriveRandomness(&fs, "gamma")
	if err != nil {
		return proof, err
	}

	// Compute to Z
	lz := evaluateAccumulationPolynomial(lf, lt, lh1, lh2, beta, gamma)
	cz := make([]fr.Element, len(lz))
	copy(cz, lz)
	domainSmall.FFTInverse(cz, fft.DIF)
	fft.BitReverse(cz)
	proof.z, err = kzg.Commit(cz, srs)
	if err != nil {
		return proof, err
	}

	// prepare data for computing the quotient
	// compute the numerator
	s := domainSmall.Cardinality
	domainBig := fft.NewDomain(uint64(2 * s))

	_lz := make([]fr.Element, 2*s)
	_lh1 := make([]fr.Element, 2*s)
	_lh2 := make([]fr.Element, 2*s)
	_lt := make([]fr.Element, 2*s)
	_lf := make([]fr.Element, 2*s)
	copy(_lz, cz)
	copy(_lh1, ch1)
	copy(_lh2, ch2)
	copy(_lt, ct)
	copy(_lf, cf)
	domainBig.FFT(_lz, fft.DIF, true)
	domainBig.FFT(_lh1, fft.DIF, true)
	domainBig.FFT(_lh2, fft.DIF, true)
	domainBig.FFT(_lt, fft.DIF, true)
	domainBig.FFT(_lf, fft.DIF, true)

	// compute h
	lh := evaluateNumBitReversed(_lz, _lh1, _lh2, _lt, _lf, beta, gamma, domainBig) // CORRECT

	// compute l0*(z-1)
	lh0 := evaluateZStartsByOneBitReversed(_lz, domainBig) // CORRECT

	// compute ln(z-1)
	lhn := evaluateZEndsByOneBitReversed(_lz, domainBig) // CORRECT

	// compute ln*(h1-h2(g*X))
	lh1h2 := evaluateOverlapH1h2BitReversed(_lh1, _lh2, domainBig)

	// compute the quotient
	alpha, err := deriveRandomness(&fs, "alpha", &proof.z)
	if err != nil {
		return proof, err
	}
	ch := computeQuotientCanonical(alpha, lh, lh0, lhn, lh1h2, domainBig)
	proof.h, err = kzg.Commit(ch, srs)
	if err != nil {
		return proof, err
	}

	// build the opening proofs
	nu, err := deriveRandomness(&fs, "nu", &proof.h)
	if err != nil {
		return proof, err
	}
	proof.BatchedProof, err = kzg.BatchOpenSinglePoint(
		[]polynomial.Polynomial{
			ch1,
			ch2,
			ct,
			cz,
			cf,
			ch,
		},
		[]kzg.Digest{
			proof.h1,
			proof.h2,
			proof.t,
			proof.z,
			proof.f,
			proof.h,
		},
		&nu,
		hFunc,
		domainSmall,
		srs,
	)
	if err != nil {
		return proof, err
	}

	nu.Mul(&nu, &domainSmall.Generator)
	proof.BatchedProofShifted, err = kzg.BatchOpenSinglePoint(
		[]polynomial.Polynomial{
			ch1,
			ch2,
			ct,
			cz,
		},
		[]kzg.Digest{
			proof.h1,
			proof.h2,
			proof.t,
			proof.z,
		},
		&nu,
		hFunc,
		domainSmall,
		srs,
	)
	if err != nil {
		return proof, err
	}

	return proof, nil
}

// VerifyLookupVector verifies that a ProofLookupVector proof is correct
func VerifyLookupVector(srs *kzg.SRS, proof ProofLookupVector) error {

	// hash function that is used for Fiat Shamir
	hFunc := sha256.New()

	// transcript to derive the challenge
	fs := fiatshamir.NewTranscript(hFunc, "beta", "gamma", "alpha", "nu")

	// derive the various challenges
	beta, err := deriveRandomness(&fs, "beta", &proof.t, &proof.f, &proof.h1, &proof.h2)
	if err != nil {
		return err
	}

	gamma, err := deriveRandomness(&fs, "gamma")
	if err != nil {
		return err
	}

	alpha, err := deriveRandomness(&fs, "alpha", &proof.z)
	if err != nil {
		return err
	}

	nu, err := deriveRandomness(&fs, "nu", &proof.h)
	if err != nil {
		return err
	}

	// check opening proofs
	err = kzg.BatchVerifySinglePoint(
		[]kzg.Digest{
			proof.h1,
			proof.h2,
			proof.t,
			proof.z,
			proof.f,
			proof.h,
		},
		&proof.BatchedProof,
		hFunc,
		srs,
	)
	if err != nil {
		return err
	}

	err = kzg.BatchVerifySinglePoint(
		[]kzg.Digest{
			proof.h1,
			proof.h2,
			proof.t,
			proof.z,
		},
		&proof.BatchedProofShifted,
		hFunc,
		srs,
	)
	if err != nil {
		return err
	}

	// check polynomial relation using Schwartz Zippel
	var lhs, rhs, nun, g, _g, a, v, w, one fr.Element
	d := fft.NewDomain(proof.size) // only there to access to root of 1...
	one.SetOne()
	g.Exp(d.Generator, big.NewInt(int64(d.Cardinality-1)))

	v.Add(&one, &beta)
	w.Mul(&v, &gamma)

	// h(nu) where
	// h = (x-1)*z*(1+beta)*(gamma+f)*(gamma(1+beta) + t+ beta*t(gX)) -
	//		(x-1)*z(gX)*(gamma(1+beta) + h1 + beta*h1(gX))*(gamma(1+beta) + h2 + beta*h2(gX) )
	lhs.Sub(&nu, &g).
		Mul(&lhs, &proof.BatchedProof.ClaimedValues[3]).
		Mul(&lhs, &v)
	a.Add(&gamma, &proof.BatchedProof.ClaimedValues[4])
	lhs.Mul(&lhs, &a)
	a.Mul(&beta, &proof.BatchedProofShifted.ClaimedValues[2]).
		Add(&a, &proof.BatchedProof.ClaimedValues[2]).
		Add(&a, &w)
	lhs.Mul(&lhs, &a)

	rhs.Sub(&nu, &g).
		Mul(&rhs, &proof.BatchedProofShifted.ClaimedValues[3])
	a.Mul(&beta, &proof.BatchedProofShifted.ClaimedValues[0]).
		Add(&a, &proof.BatchedProof.ClaimedValues[0]).
		Add(&a, &w)
	rhs.Mul(&rhs, &a)
	a.Mul(&beta, &proof.BatchedProofShifted.ClaimedValues[1]).
		Add(&a, &proof.BatchedProof.ClaimedValues[1]).
		Add(&a, &w)
	rhs.Mul(&rhs, &a)

	lhs.Sub(&lhs, &rhs)

	// check consistancy of bounds
	var l0, ln, d1, d2 fr.Element
	l0.Exp(nu, big.NewInt(int64(d.Cardinality))).Sub(&l0, &one)
	ln.Set(&l0)
	d1.Sub(&nu, &one)
	d2.Sub(&nu, &g)
	l0.Div(&l0, &d1)
	ln.Div(&ln, &d2)

	// l0*(z-1)
	var l0z fr.Element
	l0z.Sub(&proof.BatchedProof.ClaimedValues[3], &one).
		Mul(&l0z, &l0)

	// ln*(z-1)
	var lnz fr.Element
	lnz.Sub(&proof.BatchedProof.ClaimedValues[3], &one).
		Mul(&ln, &lnz)

	// ln*(h1 - h2(g.x))
	var lnh1h2 fr.Element
	lnh1h2.Sub(&proof.BatchedProof.ClaimedValues[0], &proof.BatchedProofShifted.ClaimedValues[1]).
		Mul(&lnh1h2, &ln)

	// fold the numerator
	lnh1h2.Mul(&lnh1h2, &alpha).
		Add(&lnh1h2, &lnz).
		Mul(&lnh1h2, &alpha).
		Add(&lnh1h2, &l0z).
		Mul(&lnh1h2, &alpha).
		Add(&lnh1h2, &lhs)

	// (x**n-1) * h(x) evaluated at nu
	nun.Exp(nu, big.NewInt(int64(d.Cardinality)))
	_g.Sub(&nun, &one)
	_g.Mul(&proof.BatchedProof.ClaimedValues[5], &_g)
	if !lnh1h2.Equal(&_g) {
		return ErrPlookupVerification
	}

	return nil
}
