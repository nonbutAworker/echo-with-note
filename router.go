package echo

import (
	"net/http"
)

type (
	// Router is the registry of all registered routes for an `Echo` instance for
	// request matching and URL path parameter parsing.
	//一个echo实例对应一个router结构，此实例中的所有路由（不论是在哪一级的路由注册）都会放到这个结构体中
	Router struct {
		//前缀树（变种字典树？）
		tree *node
		//方法+路径 => *route
		routes map[string]*Route
		//无限循环套娃？
		echo *Echo
	}
	node struct {
		//路由类型：静态路由，参数路由，任意路由
		kind kind
		//标签：前缀的第一个字符的ASCCI码值
		label byte
		//前缀
		prefix string
		//父节点
		parent *node
		//静态路由的子节点 是一个数组
		staticChildren children
		//原始路径 没有经过任何处理的
		ppath string
		//参数名，在参数路由和任意路由 的时候会用到
		pnames []string
		//方法句柄：保存了 方法=>处理函数 的映射关系
		methodHandler *methodHandler
		//参数路由的子节点，为什么这个只有一个，不能是一个数组吗？
		paramChild *node
		//任意路由的子节点
		anyChild *node
		// isLeaf indicates that node does not have child routes
		//此节点是否是叶子节点 只有当三种孩子节点都不存在时才为true
		isLeaf bool
		// isHandler indicates that node has at least one handler registered to it
		isHandler bool
	}
	kind          uint8
	children      []*node
	methodHandler struct {
		connect  HandlerFunc
		delete   HandlerFunc
		get      HandlerFunc
		head     HandlerFunc
		options  HandlerFunc
		patch    HandlerFunc
		post     HandlerFunc
		propfind HandlerFunc
		put      HandlerFunc
		trace    HandlerFunc
		report   HandlerFunc
	}
)

const (
	staticKind kind = iota
	paramKind
	anyKind

	paramLabel = byte(':')
	anyLabel   = byte('*')
)

//return true indicates that node has at least one handler registered to it
//return false indicates that node has no handler
func (m *methodHandler) isHandler() bool {
	return m.connect != nil ||
		m.delete != nil ||
		m.get != nil ||
		m.head != nil ||
		m.options != nil ||
		m.patch != nil ||
		m.post != nil ||
		m.propfind != nil ||
		m.put != nil ||
		m.trace != nil ||
		m.report != nil
}

// NewRouter returns a new Router instance.
func NewRouter(e *Echo) *Router {
	return &Router{
		tree: &node{
			methodHandler: new(methodHandler),
		},
		routes: map[string]*Route{},
		echo:   e,
	}
}

// Add registers a new route for method and path with matching handler.
func (r *Router) Add(method, path string, h HandlerFunc) {
	// Validate path

	//路径必须以/开头，这样可以保证从第二个路由注册的时候就至少可以以 "/" 作为公共前缀
	if path == "" {
		path = "/"
	}
	if path[0] != '/' {
		path = "/" + path
	}
	pnames := []string{} // Param names
	ppath := path        // Pristine path

	if h == nil && r.echo.Logger != nil {
		// FIXME: in future we should return error
		// 如果没有handler函数不报错继续执行，但是没有意义，会在insert中判断，如果没有处理函数就不会加入到methhandler中
		r.echo.Logger.Errorf("Adding route without handler function: %v:%v", method, path)
	}

	//处理参数路由，和任意路由
	//example:  /admin/:name/info
	for i, lcpIndex := 0, len(path); i < lcpIndex; i++ {
		if path[i] == ':' {
			j := i + 1

			// 这里把/admin/当做静态路由的路径插入
			r.insert(method, path[:i], nil, staticKind, "", nil)
			for ; i < lcpIndex && path[i] != '/'; i++ {
			}

			//拿出来从(:,/)之间的字符串作为参数名
			pnames = append(pnames, path[j:i])
			//从原始路径中去除(:,/)之间的字符串，用于继续判断第一个参数变量之后是不是还有其他的参数
			path = path[:j] + path[i:]
			i, lcpIndex = j, len(path)

			if i == lcpIndex {
				// path node is last fragment of route path. ie. `/users/:id`
				r.insert(method, path[:i], h, paramKind, ppath, pnames)
			} else {
				// path node is not last fragment for router path. ie.`/users/:id/info`
				//path[:i] := "/admin/api/v1/tenants/:"
				r.insert(method, path[:i], nil, paramKind, "", nil)
			}
		} else if path[i] == '*' {
			r.insert(method, path[:i], nil, staticKind, "", nil)
			pnames = append(pnames, "*")
			r.insert(method, path[:i+1], h, anyKind, ppath, pnames)
		}
	}

	//如果是静态路由，直接插入字典树中
	//如果是其他路由，拿出参数后作为静态路由插入字典树中
	//path := "/admin/api/v1/tenants/:/info"
	//ppath := "/admin/api/v1/tenants/:name/info"
	//pnames := "name"
	r.insert(method, path, h, staticKind, ppath, pnames)
}

func (r *Router) insert(method, path string, h HandlerFunc, t kind, ppath string, pnames []string) {
	// Adjust max param
	paramLen := len(pnames)
	//如果当前的最大参数长度小于 这个路由的长度，就调整最大参数长度
	//既然如此为什么不干脆设置一个最大值呢？比如maxint
	if *r.echo.maxParam < paramLen {
		*r.echo.maxParam = paramLen
	}

	//把当前节点设置为root节点，也就是从根节点开始找，直到找到加入·此路由·的合适位置
	currentNode := r.tree // Current node as root
	if currentNode == nil {
		panic("echo: invalid method")
	}
	search := path

	//每次循环都找出当前路径和当前节点（刚进入的时候是根节点）的最长公共前缀
	//去除最长公共前缀 剩余的路径 再从当前节点（刚进入的时候是根节点）根据当前剩余路径的第一个字符的ASCII码值 找是否有能匹配到子节点
	//如果匹配到了子节点 说明当前这个路径 要插入的位置 还没有找到，所以continue进入下一次for循环，当前节点也被更新
	//直到当前节点的子节点 没法在能匹配到 当前的剩余路径了 说明找到了这个路径应该插入的位置
	//新建节点：把当前节点设置为 ·此路径对应节点·的父节点 并且设置path和ppname等字段，设置 ·此路径对应节点·的子节点 ，是否为叶子节点等
	//同时设置methodhandler,在当前节点（路径） 中 一个方法 对应 不同的处理函数
	for {
		searchLen := len(search)
		prefixLen := len(currentNode.prefix)
		lcpLen := 0

		// LCP - Longest Common Prefix (https://en.wikipedia.org/wiki/LCP_array)
		// max取他们两个之间小的那个，因为最长公共前缀长度 最长也只能等于 2个字符串中 更短的那个
		// 那为什么不直接用max:=min(searchlen,prefixlen) ？
		max := prefixLen
		if searchLen < max {
			max = searchLen
		}
		for ; lcpLen < max && search[lcpLen] == currentNode.prefix[lcpLen]; lcpLen++ {
		}
		//因为在Add函数中，如果路径中第一个字符不是/ 就会加上
		//所以 这个判断条件当且仅当 在第一个路由被注册的时候（也就是currentNode还是初始化的那些值的时候） 会触发
		//因为 任何 在 不是第一个加入的路由 都至少会有一个公共前缀 也就是/
		if lcpLen == 0 {
			// At root node
			currentNode.label = search[0]
			currentNode.prefix = search
			if h != nil {
				currentNode.kind = t
				currentNode.addHandler(method, h)
				currentNode.ppath = ppath
				currentNode.pnames = pnames
			}
			currentNode.isLeaf = currentNode.staticChildren == nil && currentNode.paramChild == nil && currentNode.anyChild == nil
		} else if lcpLen < prefixLen {
			//example1: 当前节点 和 搜索节点 提取公共前缀 做同级
			//prefixpath:= /portal/api/v1
			//seaechpath:= /admin/api/v1
			//example2: 当前节点 做 搜做节点的子节点
			//prefixpath := /admin/api/v1
			//searchpath := /admin
			//但是不论哪种场景 都必须要 新建一个当前节点的复制（仅修改prefix）并重置当前节点，因为当前节点的值是指向根节点的指针，不应该更改地址，只需要更改他索引的值
			// Split node
			n := newNode(
				currentNode.kind,
				currentNode.prefix[lcpLen:],
				currentNode,
				currentNode.staticChildren,
				currentNode.methodHandler,
				currentNode.ppath,
				currentNode.pnames,
				currentNode.paramChild,
				currentNode.anyChild,
			)
			// Update parent path for all children to new node
			for _, child := range currentNode.staticChildren {
				child.parent = n
			}
			if currentNode.paramChild != nil {
				currentNode.paramChild.parent = n
			}
			if currentNode.anyChild != nil {
				currentNode.anyChild.parent = n
			}

			// Reset parent node
			currentNode.kind = staticKind
			currentNode.label = currentNode.prefix[0]
			currentNode.prefix = currentNode.prefix[:lcpLen]
			currentNode.staticChildren = nil
			currentNode.methodHandler = new(methodHandler)
			currentNode.ppath = ""
			currentNode.pnames = nil
			currentNode.paramChild = nil
			currentNode.anyChild = nil
			currentNode.isLeaf = false
			currentNode.isHandler = false

			// Only Static children could reach here
			currentNode.addStaticChild(n)

			if lcpLen == searchLen {
				// At parent node
				currentNode.kind = t
				currentNode.addHandler(method, h)
				currentNode.ppath = ppath
				currentNode.pnames = pnames
			} else {
				// Create child node
				n = newNode(t, search[lcpLen:], currentNode, nil, new(methodHandler), ppath, pnames, nil, nil)
				n.addHandler(method, h)
				// Only Static children could reach here
				currentNode.addStaticChild(n)
			}
			currentNode.isLeaf = currentNode.staticChildren == nil && currentNode.paramChild == nil && currentNode.anyChild == nil
		} else if lcpLen < searchLen {
			//example1:
			//prefixpath := /admin
			//searchpath := /admin/api/v1
			//这里是prefixlen =< lcplen < searchlen 表示当前节点的前缀是搜索路径的前缀 也就是说搜索路径 至多 是当前节点的子节点，但是也有可能是子节点的子节点，所以继续往下层找
			search = search[lcpLen:]
			//通过label查找是否有合适的子节点
			//label记录了当前节点的前缀的第一个字符的ASCCI码值
			c := currentNode.findChildWithLabel(search[0])
			if c != nil {
				// Go deeper
				//如果 通过label能找到一个子节点，说明这时候 当前路径 与 这个子节点的前缀至少有一个字符是匹配的
				currentNode = c
				continue
			}
			// Create child node
			n := newNode(t, search, currentNode, nil, new(methodHandler), ppath, pnames, nil, nil)
			// 在新生成的节点中加入 method和对应的 处理函数映射
			n.addHandler(method, h)
			//这里是把 新创建的node 加入到 当前节点的孩子节点
			switch t {
			case staticKind:
				currentNode.addStaticChild(n)
			case paramKind:
				currentNode.paramChild = n
			case anyKind:
				currentNode.anyChild = n
			}
			currentNode.isLeaf = currentNode.staticChildren == nil && currentNode.paramChild == nil && currentNode.anyChild == nil
		} else {
			// Node already exists
			if h != nil {
				currentNode.addHandler(method, h)
				currentNode.ppath = ppath
				if len(currentNode.pnames) == 0 { // Issue #729
					currentNode.pnames = pnames
				}
			}
		}
		return
	}
}

func newNode(t kind, pre string, p *node, sc children, mh *methodHandler, ppath string, pnames []string, paramChildren, anyChildren *node) *node {
	return &node{
		kind:           t,
		label:          pre[0],
		prefix:         pre,
		parent:         p,
		staticChildren: sc,
		ppath:          ppath,
		pnames:         pnames,
		methodHandler:  mh,
		paramChild:     paramChildren,
		anyChild:       anyChildren,
		isLeaf:         sc == nil && paramChildren == nil && anyChildren == nil,
		isHandler:      mh.isHandler(),
	}
}

//添加节点c到n的静态子节点
func (n *node) addStaticChild(c *node) {
	n.staticChildren = append(n.staticChildren, c)
}

//根据label查找节点n的·静态子节点·
func (n *node) findStaticChild(l byte) *node {
	for _, c := range n.staticChildren {
		if c.label == l {
			return c
		}
	}
	return nil
}

//根据label查找节点n的·所有子节点·
func (n *node) findChildWithLabel(l byte) *node {
	for _, c := range n.staticChildren {
		if c.label == l {
			return c
		}
	}
	if l == paramLabel {
		return n.paramChild
	}
	if l == anyLabel {
		return n.anyChild
	}
	return nil
}

//向节点n中添加 method-> handlerfunc 的映射
func (n *node) addHandler(method string, h HandlerFunc) {
	switch method {
	case http.MethodConnect:
		n.methodHandler.connect = h
	case http.MethodDelete:
		n.methodHandler.delete = h
	case http.MethodGet:
		n.methodHandler.get = h
	case http.MethodHead:
		n.methodHandler.head = h
	case http.MethodOptions:
		n.methodHandler.options = h
	case http.MethodPatch:
		n.methodHandler.patch = h
	case http.MethodPost:
		n.methodHandler.post = h
	case PROPFIND:
		n.methodHandler.propfind = h
	case http.MethodPut:
		n.methodHandler.put = h
	case http.MethodTrace:
		n.methodHandler.trace = h
	case REPORT:
		n.methodHandler.report = h
	}

	if h != nil {
		n.isHandler = true
	} else {
		n.isHandler = n.methodHandler.isHandler()
	}
}

//在节点n中根据method查找是否有对应的处理函数
func (n *node) findHandler(method string) HandlerFunc {
	switch method {
	case http.MethodConnect:
		return n.methodHandler.connect
	case http.MethodDelete:
		return n.methodHandler.delete
	case http.MethodGet:
		return n.methodHandler.get
	case http.MethodHead:
		return n.methodHandler.head
	case http.MethodOptions:
		return n.methodHandler.options
	case http.MethodPatch:
		return n.methodHandler.patch
	case http.MethodPost:
		return n.methodHandler.post
	case PROPFIND:
		return n.methodHandler.propfind
	case http.MethodPut:
		return n.methodHandler.put
	case http.MethodTrace:
		return n.methodHandler.trace
	case REPORT:
		return n.methodHandler.report
	default:
		return nil
	}
}

//查找当前节点是否 至少存在一种方法 有对应的处理函数
//	如果有 则返表示想要找的路由存在，但是方法不对 返回·此方法不允许的处理函数·
//	如果没有 则表示想要找的路由不存在任何已经注册的方法 返回·不存在的处理函数·
func (n *node) checkMethodNotAllowed() HandlerFunc {
	for _, m := range methods {
		if h := n.findHandler(m); h != nil {
			return MethodNotAllowedHandler
		}
	}
	return NotFoundHandler
}

// Find lookup a handler registered for method and path. It also parses URL for path
// parameters and load them into context.
//
// For performance:
//
// - Get context from `Echo#AcquireContext()`
// - Reset it `Context#Reset()`
// - Return it `Echo#ReleaseContext()`.

//method GET,POST
//path /admin/api/v1
//c zero value of context
func (r *Router) Find(method, path string, c Context) {
	ctx := c.(*context)
	ctx.path = path
	currentNode := r.tree // Current node as root

	var (
		previousBestMatchNode *node
		matchedHandler        HandlerFunc
		// search stores the remaining path to check for match. By each iteration we move from start of path to end of the path
		// and search value gets shorter and shorter.
		search      = path
		searchIndex = 0
		paramIndex  int           // Param counter
		paramValues = ctx.pvalues // Use the internal slice so the interface can keep the illusion of a dynamic slice
	)

	// Backtracking is needed when a dead end (leaf node) is reached in the router tree.
	// To backtrack the current node will be changed to the parent node and the next kind for the
	// router logic will be returned based on fromKind or kind of the dead end node (static > param > any).
	// For example if there is no static node match we should check parent next sibling by kind (param).
	// Backtracking itself does not check if there is a next sibling, this is done by the router logic.
	backtrackToNextNodeKind := func(fromKind kind) (nextNodeKind kind, valid bool) {
		//previous记录当前的节点
		previous := currentNode
		//当前节点指向 当前节点的父节点
		currentNode = previous.parent
		//valid indicats if parent of currentnod exsit
		valid = currentNode != nil

		// Next node type by priority (static > param > any)
		if previous.kind == anyKind {
			nextNodeKind = staticKind
		} else {
			nextNodeKind = previous.kind + 1
		}

		if fromKind == staticKind {
			// when backtracking is done from static kind block we did not change search so nothing to restore
			return
		}

		// restore search to value it was before we move to current node we are backtracking from.
		if previous.kind == staticKind {
			searchIndex -= len(previous.prefix)
		} else {
			paramIndex--
			// for param/any node.prefix value is always `:` so we can not deduce searchIndex from that and must use pValue
			// for that index as it would also contain part of path we cut off before moving into node we are backtracking from
			searchIndex -= len(paramValues[paramIndex])
			paramValues[paramIndex] = ""
		}
		search = path[searchIndex:]
		return
	}

	// Router tree is implemented by longest common prefix array (LCP array) https://en.wikipedia.org/wiki/LCP_array
	// Tree search is implemented as for loop where one loop iteration is divided into 3 separate blocks
	// Each of these blocks checks specific kind of node (static/param/any). Order of blocks reflex their priority in routing.
	// Search order/priority is: static > param > any.
	//
	// Note: backtracking in tree is implemented by replacing/switching currentNode to previous node
	// and hoping to (goto statement) next block by priority to check if it is the match.
	for {
		prefixLen := 0 // Prefix length
		lcpLen := 0    // LCP (longest common prefix) length

		if currentNode.kind == staticKind {
			//searchpath := /admin/api/v1
			//prefixpath := /admin/api/v2
			searchLen := len(search)
			prefixLen = len(currentNode.prefix)

			// LCP - Longest Common Prefix (https://en.wikipedia.org/wiki/LCP_array)
			max := prefixLen
			if searchLen < max {
				max = searchLen
			}
			for ; lcpLen < max && search[lcpLen] == currentNode.prefix[lcpLen]; lcpLen++ {
			}
		}

		if lcpLen != prefixLen {
			// No matching prefix, let's backtrack to the first possible alternative node of the decision path
			nk, ok := backtrackToNextNodeKind(staticKind)
			if !ok {
				return // No other possibilities on the decision path
			} else if nk == paramKind {
				goto Param
				// NOTE: this case (backtracking from static node to previous any node) can not happen by current any matching logic. Any node is end of search currently
				//} else if nk == anyKind {
				//	goto Any
			} else {
				// Not found (this should never be possible for static node we are looking currently)
				break
			}
		}

		// The full prefix has matched, remove the prefix from the remaining search
		search = search[lcpLen:]
		searchIndex = searchIndex + lcpLen

		// Finish routing if no remaining search and we are on a node with handler and matching method type
		if search == "" && currentNode.isHandler {
			// check if current node has handler registered for http method we are looking for. we store currentNode as
			// best matching in case we do no find no more routes matching this path+method
			if previousBestMatchNode == nil {
				previousBestMatchNode = currentNode
			}
			if h := currentNode.findHandler(method); h != nil {
				matchedHandler = h
				break
			}
		}

		// Static node
		if search != "" {
			if child := currentNode.findStaticChild(search[0]); child != nil {
				currentNode = child
				continue
			}
		}

	Param:
		//searchpath := /user/:name
		//prefixpat := /user
		// Param node
		//这里的currentNode已经回退到上一层了
		if child := currentNode.paramChild; search != "" && child != nil {
			currentNode = child
			i := 0
			l := len(search)
			if currentNode.isLeaf {
				// when param node does not have any children then param node should act similarly to any node - consider all remaining search as match
				i = l
			} else {
				for ; i < l && search[i] != '/'; i++ {
				}
			}

			paramValues[paramIndex] = search[:i]
			paramIndex++
			search = search[i:]
			searchIndex = searchIndex + i
			continue
		}

	Any:
		// Any node
		if child := currentNode.anyChild; child != nil {
			// If any node is found, use remaining path for paramValues
			currentNode = child
			paramValues[len(currentNode.pnames)-1] = search
			// update indexes/search in case we need to backtrack when no handler match is found
			paramIndex++
			searchIndex += +len(search)
			search = ""

			// check if current node has handler registered for http method we are looking for. we store currentNode as
			// best matching in case we do no find no more routes matching this path+method
			if previousBestMatchNode == nil {
				previousBestMatchNode = currentNode
			}
			if h := currentNode.findHandler(method); h != nil {
				matchedHandler = h
				break
			}
		}

		// Let's backtrack to the first possible alternative node of the decision path
		nk, ok := backtrackToNextNodeKind(anyKind)
		if !ok {
			break // No other possibilities on the decision path
		} else if nk == paramKind {
			goto Param
		} else if nk == anyKind {
			goto Any
		} else {
			// Not found
			break
		}
	}

	if currentNode == nil && previousBestMatchNode == nil {
		return // nothing matched at all
	}

	if matchedHandler != nil {
		ctx.handler = matchedHandler
	} else {
		// use previous match as basis. although we have no matching handler we have path match.
		// so we can send http.StatusMethodNotAllowed (405) instead of http.StatusNotFound (404)
		currentNode = previousBestMatchNode
		ctx.handler = currentNode.checkMethodNotAllowed()
	}
	ctx.path = currentNode.ppath
	ctx.pnames = currentNode.pnames

	return
}
