// Hand-written supplement to the generated stubs; added alongside the Cancel
// RPC (issue #198).  When buf regeneration becomes available, remove this file
// and let protoc-gen-go produce the types from the updated .proto.

package toolv1

import (
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	"reflect"
	"sync"
)

// CancelRequest is sent by the host to cancel an in-flight Call.
// call_id is the value the host previously injected via gRPC metadata
// "gleipnir-call-id" (spec §8.5).
type CancelRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	CallId        string                 `protobuf:"bytes,1,opt,name=call_id,json=callId,proto3" json:"call_id,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *CancelRequest) Reset() {
	*x = CancelRequest{}
	mi := &file_cancel_msgTypes[0]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}
func (x *CancelRequest) String() string { return protoimpl.X.MessageStringOf(x) }
func (*CancelRequest) ProtoMessage()    {}

func (x *CancelRequest) ProtoReflect() protoreflect.Message {
	mi := &file_cancel_msgTypes[0]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

func (x *CancelRequest) GetCallId() string {
	if x != nil {
		return x.CallId
	}
	return ""
}

// CancelResponse is returned by the plugin to acknowledge the cancel request.
// It carries no data.
type CancelResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *CancelResponse) Reset() {
	*x = CancelResponse{}
	mi := &file_cancel_msgTypes[1]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}
func (x *CancelResponse) String() string { return protoimpl.X.MessageStringOf(x) }
func (*CancelResponse) ProtoMessage()    {}

func (x *CancelResponse) ProtoReflect() protoreflect.Message {
	mi := &file_cancel_msgTypes[1]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// file_cancel_msgTypes holds MessageInfo for CancelRequest and CancelResponse.
var file_cancel_msgTypes = make([]protoimpl.MessageInfo, 2)

// file_cancel_rawDesc is the binary FileDescriptorProto for:
//
//	syntax = "proto3";
//	package gleipnir.plugin.tool.v1;
//	option go_package = "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/tool/v1;toolv1";
//	message CancelRequest  { string call_id = 1; }
//	message CancelResponse {}
//
// Computed offline from the proto source (buf generate unavailable in this
// environment). Remove this file when buf generate is run against the
// updated .proto — at that point the types will appear in tool.pb.go.
const file_cancel_rawDesc = "" +
	"\x0a\x0c\x63\x61\x6e\x63\x65\x6c\x2e\x70\x72\x6f\x74\x6f\x12\x17\x67\x6c\x65\x69\x70" +
	"\x6e\x69\x72\x2e\x70\x6c\x75\x67\x69\x6e\x2e\x74\x6f\x6f\x6c\x2e\x76\x31\x22\x28\x0a\x0d" +
	"\x43\x61\x6e\x63\x65\x6c\x52\x65\x71\x75\x65\x73\x74\x12\x17\x0a\x07\x63\x61\x6c\x6c\x5f" +
	"\x69\x64\x52\x06\x63\x61\x6c\x6c\x49\x64\x18\x01\x28\x09\x20\x01\x22\x10\x0a\x0e\x43\x61" +
	"\x6e\x63\x65\x6c\x52\x65\x73\x70\x6f\x6e\x73\x65\x42\x55\x5a\x53\x67\x69\x74\x68\x75\x62" +
	"\x2e\x63\x6f\x6d\x2f\x66\x65\x6c\x61\x67\x2d\x65\x6e\x67\x69\x6e\x65\x65\x72\x69\x6e\x67" +
	"\x2f\x67\x6c\x65\x69\x70\x6e\x69\x72\x2f\x70\x6c\x75\x67\x69\x6e\x2d\x73\x64\x6b\x2f\x67" +
	"\x65\x6e\x2f\x67\x6c\x65\x69\x70\x6e\x69\x72\x2f\x70\x6c\x75\x67\x69\x6e\x2f\x74\x6f\x6f" +
	"\x6c\x2f\x76\x31\x3b\x74\x6f\x6f\x6c\x76\x31\x62\x06\x70\x72\x6f\x74\x6f\x33"

var (
	file_cancel_rawDescOnce sync.Once
	file_cancel_rawDescData []byte
)

func file_cancel_rawDescGZIP() []byte {
	file_cancel_rawDescOnce.Do(func() {
		file_cancel_rawDescData = protoimpl.X.CompressGZIP([]byte(file_cancel_rawDesc))
	})
	return file_cancel_rawDescData
}

var file_cancel_goTypes = []any{
	(*CancelRequest)(nil),  // 0
	(*CancelResponse)(nil), // 1
}

var file_cancel_depIdxs = []int32{
	0, // [0:0] is the sub-list for method output_type
	0, // [0:0] is the sub-list for method input_type
	0, // [0:0] is the sub-list for extension type_name
	0, // [0:0] is the sub-list for extension extendee
	0, // [0:0] is the sub-list for field type_name
}

// File_cancel_proto is the file descriptor for the hand-written cancel.proto.
var File_cancel_proto protoreflect.FileDescriptor

func init() {
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: []byte(file_cancel_rawDesc),
			NumEnums:      0,
			NumMessages:   2,
			NumExtensions: 0,
			NumServices:   0,
		},
		GoTypes:           file_cancel_goTypes,
		DependencyIndexes: file_cancel_depIdxs,
		MessageInfos:      file_cancel_msgTypes,
	}.Build()
	File_cancel_proto = out.File
	file_cancel_goTypes = nil
	file_cancel_depIdxs = nil
}
