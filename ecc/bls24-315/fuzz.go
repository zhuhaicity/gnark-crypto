// +build gofuzz

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

// Code generated by consensys/gnark-crypto DO NOT EDIT

package bls24315

import (
	"bytes"
	"github.com/consensys/gnark-crypto/ecc/bls24-315/fp"
	"github.com/consensys/gnark-crypto/ecc/bls24-315/fr"
	"github.com/consensys/gnark-crypto/ecc/bls24-315/fr/mimc"
	"math/big"
)

const (
	fuzzInteresting = 1
	fuzzNormal      = 0
	fuzzDiscard     = -1
)

func Fuzz(data []byte) int {
	// TODO separate in multiple FuzzXXX and update continuous fuzzer scripts
	// else, we don't really benefits for fuzzer strategy.
	fr.Fuzz(data)
	fp.Fuzz(data)
	mimc.Fuzz(data)

	// fuzz pairing
	r := bytes.NewReader(data)
	var e1, e2 fr.Element
	e1.SetRawBytes(r)
	e2.SetRawBytes(r)

	{
		var r, r1, r2, r1r2, zero GT
		var b1, b2, b1b2 big.Int
		e1.ToBigIntRegular(&b1)
		e2.ToBigIntRegular(&b2)
		b1b2.Mul(&b1, &b2)

		var p1 G1Affine
		var p2 G2Affine

		p1.ScalarMultiplication(&g1GenAff, &b1)
		p2.ScalarMultiplication(&g2GenAff, &b2)

		r, _ = Pair([]G1Affine{g1GenAff}, []G2Affine{g2GenAff})
		r1, _ = Pair([]G1Affine{p1}, []G2Affine{g2GenAff})
		r2, _ = Pair([]G1Affine{g1GenAff}, []G2Affine{p2})

		r1r2.Exp(&r, b1b2)
		r1.Exp(&r1, b2)
		r2.Exp(&r2, b1)

		if !(r1r2.Equal(&r1) && r1r2.Equal(&r2) && !r.Equal(&zero)) {
			panic("pairing bilinearity check failed")
		}
	}

	return fuzzNormal
}
