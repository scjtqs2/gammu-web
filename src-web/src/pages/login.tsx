import { useEffect, useState } from 'react';
import { useTranslation } from "react-i18next";
import { useParams } from "react-router-dom";
import { useNavigate } from "react-router-dom";

import { setToken as setStorageToken, getToken } from '../services/login';
import { Global } from '../services/global';
import { connect as ws_connect } from '../services/ws';
import { getPhoneInfo } from '../services/sms'
import { verifyToken, setLogStatus, checkLogStatus } from '../services/login'

export const LoginPage = () => {
  checkLogStatus();

  const { t } = useTranslation();
  const navigate = useNavigate();
  const [token, setToken] = useState(getToken());
  const [connStatus, setConnStatus] = useState(false);
  const [remember, setRemember] = useState(getToken().length > 0);

    useEffect(() => {
        // 初始化时，如果有保存的token，设置到状态中
        const savedToken = getToken();
        if (savedToken && savedToken.length > 0) {
            setToken(savedToken);
            setRemember(true);
        } else {
            setRemember(false);
        }
    }, []);

  const checkRemember = (e: any) => {
    setRemember(e.target.checked);
  };

    const connect = () => {
        setConnStatus(true);
        Global.token = token;

        // 只有在remember为true时才保存token到localStorage
        if (remember) {
            setStorageToken(token);
        } else {
            setStorageToken(''); // 清除保存的token
        }

        verifyToken().then(resp => {
            if (resp.retCode === 0) {
                setLogStatus(true); // 设置登录状态

                // 如果不记住token，只在内存中保存
                if (!remember) {
                    Global.token = token; // 只在全局变量中保存
                }

                Global.phoneInfo = { loadFlag: false };
                getPhoneInfo().then(resp => {
                    Global.phoneInfo = { loadFlag: true, ...resp.data };
                    ws_connect();
                    navigate("/sms");
                });
            } else {
                // token验证失败，重置登录状态
                setLogStatus(false);
                if (!remember) {
                    Global.token = ''; // 清除内存中的token
                }
            }
            setConnStatus(false);
        }).catch(error => {
            // 请求失败时也重置状态
            setLogStatus(false);
            if (!remember) {
                Global.token = '';
            }
            setConnStatus(false);
        });
    };

  return (<div className='container'>
    <div className="form-control w-full">
      <label className="label">
        <span className="label-text">Token</span>
      </label>
      <input type="text" value={token} className="input w-full input-bordered" onChange={(e) => setToken(e.target.value)} />
      <label className="label cursor-pointer">
        <span className="label-text">{t("Remember it")}</span>
        <input type="checkbox" defaultChecked={getToken().length > 0} className="checkbox" onChange={(e) => setRemember(e.target.checked)} />
      </label>
    </div>
    <div className="flex justify-end">
      <button className='btn' onClick={connect}>{t("Connect")}</button>
    </div>
  </div>
  );
}

export default LoginPage;