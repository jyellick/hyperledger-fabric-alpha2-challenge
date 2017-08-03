/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package main

import (
	"bytes"
	"fmt"

	"github.com/hyperledger/fabric/core/chaincode/shim"
	sc "github.com/hyperledger/fabric/protos/peer"

	"github.com/golang/protobuf/proto"
)

// Define the Smart Contract structure
type AssetRegistry struct{}

// Init is called when the chaincode is instantiatied, for now, a no-op
func (s *AssetRegistry) Init(stub shim.ChaincodeStubInterface) sc.Response {
	return shim.Success(nil)
}

// Invoke allows for the manipulation of assets.
// Possible arguments are:
//   ["create",   <asset_key>]                  // Creates a new asset
//   ["lock",     <asset_key>, <to_channel>]    // Locks the asset to another channel, disabling other manipulation of the asset
//   ["show",     <asset_key>, <from_channel>]  // Shows an asset from another channel in this channel
//   ["transfer", <asset_key>, <to_owner>]      // Transfers an asset's ownership to another identity
//   ["query",    <asset_key>]                  // Query's an asset's state
func (s *AssetRegistry) Invoke(stub shim.ChaincodeStubInterface) sc.Response {
	ac, err := newAssetContext(stub)
	if err != nil {
		return shim.Error(err.Error())
	}
	return ac.execute()
}

// parseArgs returns the function name, the key of the asset to operate on, an optional
// additional arg for the function, or an error if there are too few, or too many args
func parseArgs(args [][]byte) (function string, key string, arg []byte, err error) {
	switch len(args) {
	case 3:
		arg = args[2]
		fallthrough
	case 2:
		key = string(args[1])
		function = string(args[0])
	case 1:
		err = fmt.Errorf("Invoke called with only one argument")
	case 0:
		err = fmt.Errorf("Invoke called with no arguments")
	default:
		err = fmt.Errorf("Invoke called with too many arguments")
	}
	return
}

type assetContext struct {
	stub        shim.ChaincodeStubInterface
	creator     []byte // Guaranteed to be set
	asset       *Asset // May be nil if asset does not already exist
	function    string // The name of the operation being invoked
	key         string // The name of the asset being operated on
	functionArg []byte // The remaining arg if any to pass to the operation
}

func newAssetContext(stub shim.ChaincodeStubInterface) (*assetContext, error) {
	function, key, functionArg, err := parseArgs(stub.GetArgs())
	if err != nil {
		return nil, err
	}

	creator, err := stub.GetCreator()
	if err != nil {
		return nil, fmt.Errorf("Could not get creator: %s", err)
	}

	// All functions need to know about the current version of an asset if it exists
	assetBytes, err := stub.GetState(key)
	if err != nil {
		return nil, fmt.Errorf("Could not get asset for key %s: %s", key, err)
	}

	var asset *Asset
	if assetBytes != nil {
		asset = &Asset{}
		err = proto.Unmarshal(assetBytes, asset)
		if err != nil {
			return nil, fmt.Errorf("Unexpected error unmarshaling: %s", err)
		}
	}

	return &assetContext{
		stub:        stub,
		creator:     creator,
		asset:       asset,
		key:         key,
		function:    function,
		functionArg: functionArg,
	}, nil
}

func (ac *assetContext) execute() sc.Response {
	// Route to the appropriate handler function to interact with the ledger appropriately
	var err error
	var result []byte
	switch ac.function {
	case "create":
		result, err = ac.create()
	case "lock":
		result, err = ac.lock()
	case "show":
		result, err = ac.show()
	case "transfer":
		result, err = ac.transfer()
	case "query":
		result, err = ac.query()
	default:
		return shim.Error("Invalid invocation function")
	}

	if err != nil {
		return shim.Error(err.Error())
	}

	return shim.Success(result)
}

func (ac *assetContext) ownsAsset() bool {
	if ac.asset == nil {
		return false
	}

	if len(ac.asset.History) == 0 {
		// Reachable only through programming error
		return false
	}

	// Get the current owner
	assetOwner := ac.asset.History[len(ac.asset.History)-1]

	if assetOwner == nil {
		// Reachable only through programming error
		return false
	}

	return bytes.Equal(assetOwner.Id, ac.creator)
}

func (ac *assetContext) create() ([]byte, error) {
	if ac.functionArg != nil {
		return nil, fmt.Errorf("Too many arguments to 'create'")
	}

	if ac.asset != nil {
		return nil, fmt.Errorf("Cannot create an asset who's key already exists")
	}

	assetBytes, err := proto.Marshal(&Asset{
		LockedToChannel: "",
		History:         []*Owner{&Owner{Id: ac.creator}},
	})
	if err != nil {
		return nil, fmt.Errorf("Error marshaling proto: %s", err)
	}

	err = ac.stub.PutState(ac.key, assetBytes)
	if err != nil {
		return nil, fmt.Errorf("Could not put state for key %s: %s", ac.key, err)
	}

	return assetBytes, nil
}

func (ac *assetContext) lock() ([]byte, error) {
	if ac.functionArg == nil {
		return nil, fmt.Errorf("Must pass toChannel argument")
	}

	toChannel := string(ac.functionArg)

	if ac.asset == nil {
		return nil, fmt.Errorf("Cannot lock asset which does not exist")
	}

	if ac.asset.LockedToChannel != "" {
		return nil, fmt.Errorf("Asset %s is already locked to %s", ac.key, ac.asset.LockedToChannel)
	}

	if !ac.ownsAsset() {
		return nil, fmt.Errorf("Not authorized to lock asset %s", ac.key)
	}

	// XXX Should we check to see if we know about this channel? This would
	// be a sanity check, but not necessary for correctness

	ac.asset.LockedToChannel = toChannel

	assetBytes, err := proto.Marshal(ac.asset)
	if err != nil {
		return nil, fmt.Errorf("Error marshaling proto: %s", err)
	}

	err = ac.stub.PutState(ac.key, assetBytes)
	if err != nil {
		return nil, fmt.Errorf("Could not put state for key %s: %s", ac.key, err)
	}

	return assetBytes, nil
}

func (ac *assetContext) show() ([]byte, error) {
	if ac.functionArg == nil {
		return nil, fmt.Errorf("Must pass fromChannel argument")
	}

	fromChannel := string(ac.functionArg)

	if ac.asset != nil && ac.asset.LockedToChannel == "" {
		return nil, fmt.Errorf("Cannot show an extant unlocked asset")
	}

	// TODO perform cross channel query based on 'fromChannel'
	// as a hack for now, we always assume the asset existed in the fromChannel
	_ = fromChannel
	fromAsset := &Asset{
		History: []*Owner{
			&Owner{Id: ac.creator},
		},
	}

	if ac.asset != nil {
		if len(ac.asset.History) >= len(fromAsset.History) {
			return nil, fmt.Errorf("Asset has already been shown with newer history")
		}
	}

	toAssetBytes, err := proto.Marshal(&Asset{
		LockedToChannel: "",
		History:         append(fromAsset.History, fromAsset.History[len(fromAsset.History)-1]),
	})
	if err != nil {
		return nil, fmt.Errorf("Error marshaling proto: %s", err)
	}

	err = ac.stub.PutState(ac.key, toAssetBytes)
	if err != nil {
		return nil, fmt.Errorf("Could not put state for key %s: %s", ac.key, err)
	}

	return toAssetBytes, nil
}

func (ac *assetContext) transfer() ([]byte, error) {
	if ac.functionArg == nil {
		return nil, fmt.Errorf("Must pass target to transfer to")
	}

	toID := ac.functionArg

	if ac.asset == nil {
		return nil, fmt.Errorf("Cannot transfer an asset which does not exist")
	}

	if ac.asset.LockedToChannel != "" {
		return nil, fmt.Errorf("Cannot transfer an asset which has been locked to another channel")
	}

	if !ac.ownsAsset() {
		return nil, fmt.Errorf("Not authorized to transfer asset %s", ac.key)
	}

	ac.asset.History = append(ac.asset.History, &Owner{Id: toID})

	assetBytes, err := proto.Marshal(ac.asset)
	if err != nil {
		return nil, fmt.Errorf("Error marshaling proto: %s", err)
	}

	err = ac.stub.PutState(ac.key, assetBytes)
	if err != nil {
		return nil, fmt.Errorf("Could not put state for key %s: %s", ac.key, err)
	}

	return assetBytes, nil
}

func (ac *assetContext) query() ([]byte, error) {
	if ac.functionArg == nil {
		return nil, fmt.Errorf("Too many args to 'query'")
	}

	if ac.asset == nil {
		return nil, fmt.Errorf("No asset found")
	}

	assetBytes, err := proto.Marshal(ac.asset)
	if err != nil {
		return nil, fmt.Errorf("Error marshaling proto: %s", err)
	}

	return assetBytes, nil
}

// main function starts up the chaincode in the container during instantiate
func main() {
	if err := shim.Start(new(AssetRegistry)); err != nil {
		fmt.Printf("Error starting AssetRegistry chaincode: %s", err)
	}
}
