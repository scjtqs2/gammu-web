import { Global } from './global';
import { WS, connect as ws_connect } from './ws';

export const setToken = (token: string) => {
  localStorage.setItem("token", token);
};

export const getToken = (): string => {
  return Global.token || localStorage.getItem("token") || "";
};

export const setLogStatus = (status: boolean) => {
  localStorage.setItem("logStatus", String(status));
}

export const getLogStatus = (): boolean => {
  return (localStorage.getItem("logStatus") || "false") === "true"
}

export const checkLogStatus = () => {
    let status = getLogStatus();
    const token = getToken(); // 获取当前token

    // 如果登录状态为true但没有有效的token，重置登录状态
    if (status && (!token || token.trim() === "")) {
        setLogStatus(false);
        status = false;
    }

    if (!status && window.location.pathname !== "/") {
        window.location.href = "/";
        return;
    }

    if (window.location.pathname === "/" && status) {
        if (WS == null) {
            // 确保有有效的token才进行连接
            if (token && token.trim() !== "") {
                ws_connect();
                window.location.href = "/sms";
            } else {
                setLogStatus(false); // 没有有效token，重置登录状态
            }
        } else {
            window.location.href = "/sms";
        }
    }

    if (status && WS == null && token && token.trim() !== "") {
        ws_connect();
    }
}

export const verifyToken = () => {
  return fetch(`/api/verify_token?token=${getToken()}`)
    .then(resp => resp.json())
    .catch(error => console.error(error));
}