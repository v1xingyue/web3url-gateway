package web3protocol

import (
    "context"
    "strconv"
    // "encoding/hex"
    "encoding/json"
    "fmt"
    // "net"
    "errors"
    "net/http"
    "strings"
    "time"
    "regexp"

    log "github.com/sirupsen/logrus"

    "github.com/ethereum/go-ethereum"
    "github.com/ethereum/go-ethereum/accounts/abi"
    "github.com/ethereum/go-ethereum/common"
    "github.com/ethereum/go-ethereum/ethclient"
    "github.com/ethereum/go-ethereum/crypto"
)

type Client struct {
    Config Config
    nameAddrCache *localCache
}

type DomainNameService string
const (
    DomainNameServiceENS = "ens"
    DomainNameServiceW3NS = "w3ns"
)

type ContractCallMode string
const (
    ContractCallModeCalldata = "calldata"
    ContractCallModeMethod = "method"
)

type ContractReturnProcessing string
const (
    // Expect the whole returned data to be ABI-encoded bytes. Decode.
    ContractReturnProcessingABIEncodedBytes = "decodeABIEncodedBytes"
    // JSON-encode the raw bytes of the returned data
    ContractReturnProcessingRawBytesJsonEncoded = "jsonEncodeRawBytes"
    // JSON-encode the different return values
    ContractReturnProcessingJsonEncodeValues = "jsonEncodeValues"
    // Expect a string as first return value, parse it as a dataUrl
    // ContractReturnProcessingDataUrl = "dataUrl" // To implement
    // Expect a return following the erc5219 spec, will decode it using this spec
    ContractReturnProcessingErc5219 = "erc5219"
)

type Web3URL struct {
    // The actual url string "web3://...."
    Url string

    // If the host was a domain name, what domain name service was used?
    HostDomainNameResolver DomainNameService
    // Chain of the name resolution service
    HostDomainNameResolverChainId int

    // The contract address (after optional domain name resolution) that is going to be called,
    // and its chain location
    ContractAddress common.Address // actual address
    ChainId int

    // The ERC-4804 resolve mode
    ResolveMode ResolveMode

    // How do we call the smartcontract
    // 'calldata' : We use a raw calldata
    // 'method': We use the specified method and method parameters
    ContractCallMode ContractCallMode
    // Attributes for ContractCallModeCalldata
    Calldata []byte
    // Attributes for ContractCallModeMethod
    MethodName string
    MethodArgs []abi.Type
    MethodArgValues []interface{}

    // How to process the return of the contract. See enum for doc
    ContractReturnProcessing ContractReturnProcessing
    // In case of contractReturnProcessing being decodeABIEncodedBytes,
    // this will set the mime type to return
    DecodedABIEncodedBytesMimeType string
    // In case of ContractReturnProcessing being jsonEncodeValues,
    // this will tell us how to ABI-decode the returned data
    JsonEncodedValueTypes []abi.Type
}

type FetchedWeb3URL struct {
    // The web3 URL, parsed
    ParsedUrl *Web3URL

    // The raw data returned by the contract
    ContractReturn []byte

    // The processed output, to be returned by the browser
    Output []byte
    // The HTTP code to be returned by the browser
    HttpCode int
    // The HTTP headers to be returned by the browser
    HttpHeaders map[string]string
}


func NewClient() (client *Client) {
    // Default values
    config := Config{
        NameAddrCacheDurationInMinutes: 60,
    }

    client = &Client{
        Config: config,
        nameAddrCache: newLocalCache(time.Duration(config.NameAddrCacheDurationInMinutes)*time.Minute, 10*time.Minute),
    }

    return
}

func (client *Client) FetchUrl(url string) (fetchedUrl FetchedWeb3URL, err error) {
    // Parse the URL
    parsedUrl, err := client.ParseUrl(url)
    if err != nil {
        return
    }

    // Fetch the contract return data
    contractReturn, err := client.FetchContractReturn(&parsedUrl)
    if err != nil {
        return
    }

    // Finally, process the returned data
    fetchedUrl, err = client.ProcessContractReturn(&parsedUrl, contractReturn)
    if err != nil {
        return
    }

    return
}

func (client *Client) ParseUrl(url string) (web3Url Web3URL, err error) {
    web3Url.Url = url

    web3UrlRegexp, err := regexp.Compile(`^(?P<protocol>[^:]+):\/\/(?P<hostname>[^:\/?]+)(:(?P<chainId>[1-9][0-9]*))?(?P<path>(?P<pathname>\/[^?]*)?([?](?P<searchParams>.*))?)?$`)
    if err != nil {
        return
    }
    matches := web3UrlRegexp.FindStringSubmatch(url)
    if len(matches) == 0 {
        return web3Url, &Web3Error{http.StatusBadRequest, "Invalid URL format"}
    }
    urlMainParts := map[string]string{}
    for i, name := range web3UrlRegexp.SubexpNames() {
        if i != 0 && name != "" {
            urlMainParts[name] = matches[i]
        }
    }
// fmt.Println("%+v\n", urlMainParts)

    if urlMainParts["protocol"] != "web3" {
        return web3Url, &Web3Error{http.StatusBadRequest, "Protocol name is invalid"}
    }


// var contract string
// ss := strings.Split(path, "/")
// contract = ss[1]
// web3Url.RawPath = path[len(ss[1])+1:]

    // sr[0] means all part before a potential symbol "->", split it to get chainId


    //  contract = st[0]
    //  web3Url.HostDomainNameResolverChainId = st[1]

    //  // check if chainID is valid, against cached config(can stem from a config file)
    //  _, ok := client.Config.ChainConfigs[web3Url.HostDomainNameResolverChainId]
    //  if !ok {
    //      // check if chainName is valid
    //      chainId, ok := client.Config.Name2Chain[strings.ToLower(web3Url.HostDomainNameResolverChainId)]
    //      if !ok {
    //          return web3Url, &Web3Error{http.StatusBadRequest, "unsupported chain: " + web3Url.HostDomainNameResolverChainId}
    //      }
    //      web3Url.HostDomainNameResolverChainId = chainId
    //  }
    // }

    // Default chain is ethereum mainnet
    web3Url.ChainId = 1
    if len(urlMainParts["chainId"]) > 0 {
        chainId, err := strconv.Atoi(urlMainParts["chainId"])
        if err != nil {
            // Regexp should always get us valid numbers, but we could enter here if overflow
            return web3Url, &Web3Error{http.StatusBadRequest, fmt.Sprintf("Unsupported chain %v", urlMainParts["chainId"])}
        }
        web3Url.ChainId = chainId
    }

    // Check that we support the chain
    _, ok := client.Config.ChainConfigs[web3Url.ChainId]
    if !ok {
        return web3Url, &Web3Error{http.StatusBadRequest, fmt.Sprintf("Unsupported chain %v", web3Url.ChainId)}
    }

    // after spliting from "->" and ":", var contact shall be a pure name service or a hex address
    if common.IsHexAddress(urlMainParts["hostname"]) {
        web3Url.ContractAddress = common.HexToAddress(urlMainParts["hostname"])
    } else {
        // Determine name suffix
        ss := strings.Split(urlMainParts["hostname"], ".")
        if len(ss) <= 1 {
            return web3Url, &Web3Error{http.StatusBadRequest, "Invalid contract address"}
        }
        nameServiceSuffix := ss[len(ss)-1]

        // We will use a nameservice in the current target chain
        web3Url.HostDomainNameResolverChainId = web3Url.ChainId

        chainInfo, _ := client.Config.ChainConfigs[web3Url.HostDomainNameResolverChainId]
        nsInfo, ok := chainInfo.NSConfig[nameServiceSuffix]
        if !ok {
            return web3Url, &Web3Error{http.StatusBadRequest, "Unsupported domain name service suffix: " + nameServiceSuffix}
        }

        // TODO change
        web3Url.HostDomainNameResolver = nsInfo.NSType

        var addr common.Address
        var targetChain int
        var hit bool
        cacheKey := fmt.Sprintf("%v:%v", web3Url.HostDomainNameResolverChainId, urlMainParts["hostname"])
        if client.nameAddrCache != nil {
            addr, targetChain, hit = client.nameAddrCache.get(cacheKey)
        }
        if !hit {
            var err error
            addr, targetChain, err = client.getAddressFromNameServiceWebHandler(web3Url.HostDomainNameResolverChainId, urlMainParts["hostname"])
            if err != nil {
                return web3Url, err
            }
            if client.nameAddrCache != nil {
                client.nameAddrCache.add(cacheKey, addr, targetChain)
            }
        }
        web3Url.ContractAddress = addr
        if targetChain > 0 {
            web3Url.ChainId = targetChain
        }

        _, ok = client.Config.ChainConfigs[web3Url.ChainId]
        if !ok {
            return web3Url, &Web3Error{http.StatusBadRequest, fmt.Sprintf("unsupported chain id: %v", web3Url.ChainId)}
        }
    }

    // Determine the web3 mode
    // 3 modes:
    // - Auto : we parse the path and arguments and send them
    // - Manual : we forward all the path & arguments as calldata
    // - 5219 : we parse the path and arguments and send them
    web3Url.ResolveMode = client.checkResolveMode(web3Url)

    if web3Url.ResolveMode == ResolveModeManual {
        // undecoded := req.RequestURI
        // if useSubdomain {
        //  web3Url.RawPath = undecoded
        // } else {
        //  web3Url.RawPath = undecoded[strings.Index(undecoded[1:], "/")+1:]
        // }
        err = client.parseManualModeUrl(&web3Url, urlMainParts)
    } else if web3Url.ResolveMode == ResolveModeAuto {
        err = client.parseAutoModeUrl(&web3Url, urlMainParts)
    } else if web3Url.ResolveMode == ResolveModeResourceRequests {
        // spliterIdx := strings.Index(p[1:], "/")
        // path := p[spliterIdx+1:]
        // if len(req.URL.RawQuery) > 0 {
        //  path += "?" + req.URL.RawQuery
        // }
        // bs, er = handleEIP5219(w, web3Url.Contract, web3Url.ChainId, path)
        // if er != nil {
        //  respondWithErrorPage(w, &Web3Error{http.StatusBadRequest, er.Error()})
        //  return
        // }
    }
    if err != nil {
        return
    }

    return web3Url, nil
}

func (client *Client) FetchContractReturn(web3Url *Web3URL) (contractReturn []byte, err error) {
    var calldata []byte

    // Contract call is specified with method and arguments, deduce the calldata from it
    if web3Url.ContractCallMode == ContractCallModeMethod {
        
        // ABI-encode the arguments
        abiArguments := abi.Arguments{}
        for _, methodArg := range web3Url.MethodArgs {
            abiArguments = append(abiArguments, abi.Argument{Type: methodArg})
        }
        calldataArgumentsPart, err := abiArguments.Pack(web3Url.MethodArgValues...)
        if err != nil {
            return contractReturn, err
        }

        // Determine method signature
        methodSignature := web3Url.MethodName + "("
        for i, methodArg := range web3Url.MethodArgs {
            methodSignature += methodArg.String()
            if i < len(web3Url.MethodArgs) - 1 {
                methodSignature += ","
            }
        }
        methodSignature += ")"
        methodSignatureHash := crypto.Keccak256Hash([]byte(methodSignature))

        // Compute the calldata
        calldata = append(methodSignatureHash[0:4], calldataArgumentsPart...)

    // Contract call is specified with calldata directly
    } else if web3Url.ContractCallMode == ContractCallModeCalldata {
        calldata = web3Url.Calldata
    // Empty field: This should not happen
    } else {
        err = errors.New("ContractCallMode is empty")
    }

    // Prepare the ethereum message to send
    callMessage := ethereum.CallMsg{
        From:      common.HexToAddress("0x0000000000000000000000000000000000000000"),
        To:        &web3Url.ContractAddress,
        Gas:       0,
        GasPrice:  nil,
        GasFeeCap: nil,
        GasTipCap: nil,
        Data:      calldata,
        Value:     nil,
    }
    
    // Create connection
    ethClient, err := ethclient.Dial(client.Config.ChainConfigs[web3Url.ChainId].RPC)
    if err != nil {
      return contractReturn, &Web3Error{http.StatusBadRequest, err.Error()}
    }
    defer ethClient.Close()

    // Do the contract call
    contractReturn, err = ethClient.CallContract(context.Background(), callMessage, nil)
    if err != nil {
      return contractReturn, &Web3Error{http.StatusNotFound, err.Error()}
    }

    if len(contractReturn) == 0 {
        return contractReturn, &Web3Error{http.StatusNotFound, "The contract returned no data (\"0x\").\n\nThis could be due to any of the following:\n  - The contract does not have the requested function,\n  - The parameters passed to the contract function may be invalid, or\n  - The address is not a contract."}
    }

    return
}


func (client *Client) ProcessContractReturn(web3Url *Web3URL, contractReturn []byte) (fetchedWeb3Url FetchedWeb3URL, err error) {
    // Init the maps
    fetchedWeb3Url.HttpHeaders = map[string]string{}

    if web3Url.ContractReturnProcessing == "" {
        err = errors.New("Missing ContractReturnProcessing field");
        return
    }

    // Returned data is ABI-encoded bytes: We decode them and return them
    if web3Url.ContractReturnProcessing == ContractReturnProcessingABIEncodedBytes {
        bytesType, _ := abi.NewType("bytes", "", nil)
        argsArguments := abi.Arguments{
            abi.Argument{Name: "", Type: bytesType, Indexed: false},
        }

        // Decode the ABI bytes
        unpackedValues, err := argsArguments.UnpackValues(contractReturn)
        if err != nil {
            return fetchedWeb3Url, &Web3Error{http.StatusBadRequest, "Unable to parse contract output"}
        }
        fetchedWeb3Url.Output = unpackedValues[0].([]byte)
        fetchedWeb3Url.HttpCode = 200

        // If a MIME type was hinted, inject it
        if web3Url.DecodedABIEncodedBytesMimeType != "" {
            fetchedWeb3Url.HttpHeaders["Content-Type"] = web3Url.DecodedABIEncodedBytesMimeType;
        }

    // We JSON encode the raw bytes of the returned data
    } else if web3Url.ContractReturnProcessing == ContractReturnProcessingRawBytesJsonEncoded {
        jsonEncodedOutput, err := json.Marshal([]string{fmt.Sprintf("0x%x", contractReturn)})
        if err != nil {
            return fetchedWeb3Url, err
        }
        fetchedWeb3Url.Output = jsonEncodedOutput
        fetchedWeb3Url.HttpCode = 200
        fetchedWeb3Url.HttpHeaders["Content-Type"] = "application/json";

    // Having a contract return signature, we ABI-decode it and return the result JSON-encoded
    } else if web3Url.ContractReturnProcessing == ContractReturnProcessingJsonEncodeValues {
        argsArguments := abi.Arguments{}
        for _, jsonEncodedValueType := range web3Url.JsonEncodedValueTypes {
            argsArguments = append(argsArguments, abi.Argument{Name: "", Type: jsonEncodedValueType, Indexed: false})
        }

        // Decode the ABI data
        unpackedValues, err := argsArguments.UnpackValues(contractReturn)
        if err != nil {
            return fetchedWeb3Url, &Web3Error{http.StatusBadRequest, "Unable to parse contract output"}
        }

        // Format the data
        formattedValues := make([]interface{}, 0)
        for i, arg := range argsArguments {
            // get the type of the return value
            formattedValue, err := toJSON(arg.Type, unpackedValues[i])
            if err != nil {
                return fetchedWeb3Url, err
            }
            formattedValues = append(formattedValues, formattedValue)
        }

        // JSON encode the data
        jsonEncodedOutput, err := json.Marshal(formattedValues)
        if err != nil {
            return fetchedWeb3Url, err
        }
        fetchedWeb3Url.Output = jsonEncodedOutput
        fetchedWeb3Url.HttpCode = 200
        fetchedWeb3Url.HttpHeaders["Content-Type"] = "application/json";
    }

    return
}







func addWeb3Header(w http.ResponseWriter, header string, value string) {
    w.Header().Add("Web3-"+header, value)
}

func respondWithErrorPage(w http.ResponseWriter, err Web3Error) {
    w.WriteHeader(err.HttpCode)
    _, e := fmt.Fprintf(w, "<html><h1>%d: %s</h1>%v<html/>", err.HttpCode, http.StatusText(err.HttpCode), err.Error())
    if e != nil {
        log.Errorf("Cannot write error page: %v\n", e)
        return
    }
}

func (client *Client) checkResolveMode(web3Url Web3URL) ResolveMode {
    msg, _, err := client.parseArguments(0, web3Url.ContractAddress, []string{"resolveMode"})
    if err != nil {
        panic(err)
    }
    ethClient, _ := ethclient.Dial(client.Config.ChainConfigs[web3Url.ChainId].RPC)
    defer ethClient.Close()
    bs, e := ethClient.CallContract(context.Background(), msg, nil)
    if e != nil {
        return ResolveModeAuto
    }
    if len(bs) == 32 {
        if common.Bytes2Hex(bs) == "6d616e75616c0000000000000000000000000000000000000000000000000000" {
            return ResolveModeManual
        }
        // 5219
        if common.Bytes2Hex(bs) == "3532313900000000000000000000000000000000000000000000000000000000" {
            return ResolveModeResourceRequests
        }
    }
    return ResolveModeAuto
}

// parseOutput parses the bytes into actual values according to the returnTypes string
func parseOutput(output []byte, userTypes string) ([]interface{}, error) {
    returnTypes := "(bytes)"
    if userTypes == "()" {
        return []interface{}{"0x" + common.Bytes2Hex(output)}, nil
    } else if userTypes != "" {
        returnTypes = userTypes
    }
    returnArgs := strings.Split(strings.Trim(returnTypes, "()"), ",")
    var argsArray abi.Arguments
    for _, arg := range returnArgs {
        ty, err := abi.NewType(arg, "", nil)
        if err != nil {
            return nil, &Web3Error{http.StatusBadRequest, err.Error()}
        }
        argsArray = append(argsArray, abi.Argument{Name: "", Type: ty, Indexed: false})
    }
    var res []interface{}
    res, err := argsArray.UnpackValues(output)
    if err != nil {
        return nil, &Web3Error{http.StatusBadRequest, err.Error()}
    }
    if userTypes != "" {
        for i, arg := range argsArray {
            // get the type of the return value
            res[i], _ = toJSON(arg.Type, res[i])
        }
    }
    return res, nil
}
