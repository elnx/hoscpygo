cmake_minimum_required(VERSION 3.20)
project(hoscpy_server CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_POSITION_INDEPENDENT_CODE ON)

# Prefixes produced by the static dependency build (see native/server/README.md).
# OHOS_DEPS, OHOS_GRPC_BUILD, and OHOS_GRPC_INCLUDE can be set in the
# environment or on the cmake command line.
set(OHOS_DEPS "$ENV{OHOS_DEPS}" CACHE PATH "Static OHOS dependency prefix")
if(NOT OHOS_DEPS)
  message(FATAL_ERROR "OHOS_DEPS is not set; point it at the static dependency install prefix")
endif()
if(NOT EXISTS "${OHOS_DEPS}/lib/libprotobuf.a")
  message(FATAL_ERROR "OHOS_DEPS=${OHOS_DEPS} does not contain lib/libprotobuf.a")
endif()
set(OHOS_GRPC_BUILD "$ENV{OHOS_GRPC_BUILD}" CACHE PATH "gRPC static build tree")
if(NOT OHOS_GRPC_BUILD)
  message(FATAL_ERROR "OHOS_GRPC_BUILD is not set; point it at the gRPC static build tree")
endif()
if(NOT EXISTS "${OHOS_GRPC_BUILD}/libgrpc++.a")
  message(FATAL_ERROR "OHOS_GRPC_BUILD=${OHOS_GRPC_BUILD} does not contain libgrpc++.a")
endif()
set(OHOS_GRPC_INCLUDE "$ENV{OHOS_GRPC_INCLUDE}" CACHE PATH "gRPC headers")
if(NOT OHOS_GRPC_INCLUDE)
  set(OHOS_GRPC_INCLUDE "${OHOS_DEPS}/include")
endif()

add_library(hoscpy_server SHARED
    server.cpp
    capture.cpp
    gen/scrcpy.pb.cc
)

target_include_directories(hoscpy_server PRIVATE
    ${OHOS_GRPC_INCLUDE}
    ${OHOS_DEPS}/include
    ${CMAKE_CURRENT_SOURCE_DIR}/gen
)

file(GLOB ABSL_LIBS "${OHOS_DEPS}/lib/libabsl_*.a")
file(GLOB UPB_LIBS "${OHOS_GRPC_BUILD}/libupb_*.a")

target_link_libraries(hoscpy_server PRIVATE
    "-Wl,--start-group"
    "${OHOS_GRPC_BUILD}/libgrpc++.a"
    "${OHOS_GRPC_BUILD}/libgrpc.a"
    "${OHOS_GRPC_BUILD}/libgpr.a"
    "${OHOS_GRPC_BUILD}/libaddress_sorting.a"
    ${UPB_LIBS}
    "${OHOS_DEPS}/lib/libprotobuf.a"
    "${OHOS_DEPS}/lib/libutf8_validity.a"
    "${OHOS_DEPS}/lib/libutf8_range.a"
    "${OHOS_DEPS}/lib/libre2.a"
    "${OHOS_DEPS}/lib/libcares.a"
    "${OHOS_DEPS}/lib/libssl.a"
    "${OHOS_DEPS}/lib/libcrypto.a"
    "${OHOS_DEPS}/lib/libz.a"
    ${ABSL_LIBS}
    "-Wl,--end-group"
    native_avscreen_capture
    native_media_venc
    native_media_codecbase
    native_media_core
    native_window
    native_buffer
    pthread
    dl
    rt
    m
)

target_compile_options(hoscpy_server PRIVATE -O2 -g -fvisibility=hidden)
target_link_options(hoscpy_server PRIVATE
    -static-libstdc++
    -Wl,--gc-sections
    -Wl,-soname,hoscpy_server.z.so
)

set_target_properties(hoscpy_server PROPERTIES
    OUTPUT_NAME hoscpy_server
    PREFIX ""
    SUFFIX .z.so
)
