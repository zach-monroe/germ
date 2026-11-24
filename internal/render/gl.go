package render

import (
	"fmt"
	"strings"

	"github.com/go-gl/gl/v3.3-core/gl"
)

// Quads are generated in the vertex shader from gl_VertexID (a 4-vertex
// triangle strip), so the only buffers are per-instance data.

const rectVert = `#version 330 core
layout(location = 0) in vec4 aRect;   // x, y, w, h in pixels
layout(location = 1) in vec4 aColor;
uniform vec2 uViewport;
out vec4 vColor;
void main() {
	vec2 corner = vec2(gl_VertexID & 1, gl_VertexID >> 1);
	vec2 p = (aRect.xy + corner * aRect.zw) / uViewport * 2.0 - 1.0;
	gl_Position = vec4(p.x, -p.y, 0.0, 1.0);
	vColor = aColor;
}`

const rectFrag = `#version 330 core
in vec4 vColor;
out vec4 fragColor;
void main() { fragColor = vColor; }`

const glyphVert = `#version 330 core
layout(location = 0) in vec4 aRect;   // x, y, w, h in pixels
layout(location = 1) in vec4 aUV;     // x, y, w, h in atlas pixels
layout(location = 2) in vec4 aColor;
uniform vec2 uViewport;
uniform vec2 uAtlasSize;
out vec2 vUV;
out vec4 vColor;
void main() {
	vec2 corner = vec2(gl_VertexID & 1, gl_VertexID >> 1);
	vec2 p = (aRect.xy + corner * aRect.zw) / uViewport * 2.0 - 1.0;
	gl_Position = vec4(p.x, -p.y, 0.0, 1.0);
	vUV = (aUV.xy + corner * aUV.zw) / uAtlasSize;
	vColor = aColor;
}`

const glyphFrag = `#version 330 core
in vec2 vUV;
in vec4 vColor;
uniform sampler2D uAtlas;
out vec4 fragColor;
void main() {
	fragColor = vec4(vColor.rgb, vColor.a * texture(uAtlas, vUV).r);
}`

// colorFrag draws full-color glyphs (emoji) as they are.
const colorFrag = `#version 330 core
in vec2 vUV;
in vec4 vColor;
uniform sampler2D uAtlas;
out vec4 fragColor;
void main() {
	vec4 c = texture(uAtlas, vUV);
	fragColor = vec4(c.rgb, c.a * vColor.a);
}`

func compileProgram(vertSrc, fragSrc string) (uint32, error) {
	vs, err := compileShader(vertSrc, gl.VERTEX_SHADER)
	if err != nil {
		return 0, err
	}
	defer gl.DeleteShader(vs)
	fs, err := compileShader(fragSrc, gl.FRAGMENT_SHADER)
	if err != nil {
		return 0, err
	}
	defer gl.DeleteShader(fs)

	prog := gl.CreateProgram()
	gl.AttachShader(prog, vs)
	gl.AttachShader(prog, fs)
	gl.LinkProgram(prog)
	var status int32
	gl.GetProgramiv(prog, gl.LINK_STATUS, &status)
	if status == gl.FALSE {
		return 0, fmt.Errorf("link program: %s", programLog(prog))
	}
	return prog, nil
}

func compileShader(src string, kind uint32) (uint32, error) {
	sh := gl.CreateShader(kind)
	csrc, free := gl.Strs(src + "\x00")
	gl.ShaderSource(sh, 1, csrc, nil)
	free()
	gl.CompileShader(sh)
	var status int32
	gl.GetShaderiv(sh, gl.COMPILE_STATUS, &status)
	if status == gl.FALSE {
		var n int32
		gl.GetShaderiv(sh, gl.INFO_LOG_LENGTH, &n)
		log := strings.Repeat("\x00", int(n+1))
		gl.GetShaderInfoLog(sh, n, nil, gl.Str(log))
		gl.DeleteShader(sh)
		return 0, fmt.Errorf("compile shader: %s", strings.TrimRight(log, "\x00"))
	}
	return sh, nil
}

func programLog(prog uint32) string {
	var n int32
	gl.GetProgramiv(prog, gl.INFO_LOG_LENGTH, &n)
	log := strings.Repeat("\x00", int(n+1))
	gl.GetProgramInfoLog(prog, n, nil, gl.Str(log))
	return strings.TrimRight(log, "\x00")
}

func uniform(prog uint32, name string) int32 {
	return gl.GetUniformLocation(prog, gl.Str(name+"\x00"))
}
